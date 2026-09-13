package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/awlx/mv7-linux-support/internal/meter"
	"github.com/gorilla/websocket"
)

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("meter lifecycle did not complete")
	}
}

func meterClient(s *Server) *client {
	c := &client{send: make(chan wsEvent, sendQueueLen), done: make(chan struct{})}
	s.register(c)
	return c
}

func drainEvents(c *client) []wsEvent {
	var events []wsEvent
	for {
		select {
		case event := <-c.send:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestMeterCaptureDemandAndRecovery(t *testing.T) {
	s := New(nil)
	started, stopped := make(chan struct{}, 4), make(chan struct{}, 4)
	var starts atomic.Int32
	s.meterRun = func(ctx context.Context, emit func(meter.Reading)) {
		starts.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
	}
	a, b := meterClient(s), meterClient(s)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); s.runMeter(stop) }()
	defer func() { close(stop); awaitSignal(t, done) }()
	if s.meterWanted() {
		t.Fatal("ordinary connections must not request capture")
	}
	s.setMeterSubscription(a, true)
	if s.meterWanted() {
		t.Fatal("unavailable hardware must not start audio")
	}
	setConnected := func(connected bool) {
		s.stateMu.Lock()
		s.connected = connected
		s.stateMu.Unlock()
		s.wakeMeter()
	}
	setConnected(true)
	awaitSignal(t, started)
	s.setMeterSubscription(b, true)
	s.setMeterSubscription(a, false)
	if !s.meterWanted() {
		t.Fatal("remaining subscriber must keep capture alive")
	}
	s.unregister(b)
	awaitSignal(t, stopped)
	if starts.Load() != 1 {
		t.Fatalf("two clients opened %d audio streams, want 1", starts.Load())
	}
	s.setMeterSubscription(a, true)
	awaitSignal(t, started)
	setConnected(false)
	awaitSignal(t, stopped)
	setConnected(true)
	awaitSignal(t, started)
}

func TestMeterDeliveryOnlyToSubscribersAndNeverAfterCancellation(t *testing.T) {
	s := New(nil)
	s.connected = true
	a, b := meterClient(s), meterClient(s)
	s.setMeterSubscription(a, true)
	drainEvents(a)
	drainEvents(b)
	peak, rms := -6.0206, -9.0309
	reading := meter.Reading{Available: true, PeakDBFS: &peak, RMSDBFS: &rms}
	ctx, cancel := context.WithCancel(context.Background())
	s.publishMeter(ctx, reading)
	if events := drainEvents(a); len(events) != 1 || !events[0].Meter.Available {
		t.Fatalf("subscriber did not receive reading: %+v", events)
	}
	if len(drainEvents(b)) != 0 {
		t.Fatal("non-subscriber received meter telemetry")
	}
	cancel()
	s.publishMeter(ctx, reading)
	if len(drainEvents(a)) != 0 {
		t.Fatal("cancelled capture published a late reading")
	}
	s.connected = false
	s.publishMeter(context.Background(), reading)
	if event := (<-a.send); event.Meter.Available || event.Meter.PeakDBFS != nil {
		t.Fatal("hardware loss must invalidate numeric levels")
	}
}

func TestMeterSubscriptionWireContract(t *testing.T) {
	s := New(nil)
	httpServer := httptest.NewServer(http.HandlerFunc(s.HandleWS))
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func() wsEvent {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ev wsEvent
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	status := read()
	if status.MeterSupported == nil || !*status.MeterSupported {
		t.Fatal("server must advertise metering support")
	}
	for _, params := range []string{`null`, `{}`, `{"enabled":1}`, `{"enabled":"true"}`} {
		if err := conn.WriteJSON(wsMessage{Action: "subscribe_meter", Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
		if read().Type != "error" {
			t.Fatal("invalid subscription was accepted")
		}
	}
	if err := conn.WriteJSON(wsMessage{Action: "subscribe_meter", Params: json.RawMessage(`{"enabled":true}`)}); err != nil {
		t.Fatal(err)
	}
	if ev := read(); ev.Type != "meter" || ev.Meter.Available || ev.Meter.Error == "" {
		t.Fatalf("subscription must await fresh audio: %+v", ev)
	}
	if len(s.cmds) != 0 {
		t.Fatal("meter subscription entered the HID command queue")
	}
	other, response, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://unrelated.example"}})
	if other != nil {
		other.Close()
	}
	if err == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin website can start audio capture")
	}
}
