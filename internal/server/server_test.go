package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/awlx/mv7-linux-support/internal/mv7"
)

// mockIO is a fake device that records writes and returns canned responses.
type mockIO struct {
	mu       sync.Mutex
	writes   [][]byte
	readResp []byte
}

func (m *mockIO) Write(pkt []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, append([]byte(nil), pkt...))
	return nil
}

func (m *mockIO) writtenPackets() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	packets := make([][]byte, len(m.writes))
	copy(packets, m.writes)
	return packets
}

func (m *mockIO) ReadTimeout(buf []byte, timeout time.Duration) (int, error) {
	if m.readResp == nil {
		return 0, nil
	}
	n := copy(buf, m.readResp)
	return n, nil
}

func (m *mockIO) Close() error { return nil }

// buildResp builds a valid RES_GET_FEAT response buffer for a feature.
func buildResp(feat [2]byte, value []byte) []byte {
	inner := []byte{0x11, 0x22, 0x01, 0x03, 0x08, 0x09, 0x70, 0x09,
		0x03, 0x02, 0x02, 0x00, feat[0], feat[1]}
	inner = append(inner, value...)
	// Recompute data_len fields for the actual value length.
	dataLen := byte(3 + len(value) + 2)
	inner[5] = dataLen
	inner[7] = dataLen
	crc := crc16(inner)
	buf := make([]byte, 64)
	buf[0] = 0x01
	buf[1] = byte(len(inner) + 2)
	copy(buf[2:], inner)
	buf[2+len(inner)] = byte(crc >> 8)
	buf[2+len(inner)+1] = byte(crc & 0xFF)
	return buf
}

func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		byte := b
		for i := 0; i < 8; i++ {
			bit := (crc ^ uint16(byte)) & 1
			crc >>= 1
			if bit != 0 {
				crc ^= 0xA001
			}
			byte >>= 1
		}
	}
	return crc
}

// TestCommandQueueSerializes verifies that commands from multiple clients are
// processed FIFO by the single worker and never interleave.
func TestCommandQueueSerializes(t *testing.T) {
	io := &mockIO{}
	dev := mv7.NewDevice(io)
	srv := New(dev)

	stop := make(chan struct{})
	go srv.Run(50*time.Millisecond, stop)
	defer close(stop)

	// Enqueue several commands from "different clients".
	cmds := []wsMessage{
		{Action: "set_gain", Params: json.RawMessage(`{"value":10.5}`)},
		{Action: "set_mute", Params: json.RawMessage(`{"value":true}`)},
		{Action: "set_hpf", Params: json.RawMessage(`{"value":1}`)},
		{Action: "set_tone", Params: json.RawMessage(`{"value":50}`)},
	}
	for _, c := range cmds {
		srv.cmds <- c
	}

	// Give the worker time to process.
	time.Sleep(300 * time.Millisecond)

	// The worker should have processed all 4 commands plus initial + post-command
	// refreshes. Verify the SET packets were written in order.
	var setCmds [][]byte
	for _, w := range io.writtenPackets() {
		if len(w) >= 13 && w[10] == 0x02 && w[11] == 0x02 && w[12] == 0x02 {
			setCmds = append(setCmds, w)
		}
	}
	if len(setCmds) < 4 {
		t.Fatalf("expected at least 4 SET packets, got %d", len(setCmds))
	}
	// Verify order: gain, mute, hpf, tone.
	// gain payload: [00 01 02 04 1a] (10.5 dB * 100 = 1050 = 0x041A)
	if setCmds[0][13] != 0x00 || setCmds[0][14] != 0x01 || setCmds[0][15] != 0x02 {
		t.Errorf("first SET should be gain, got % x", setCmds[0][13:16])
	}
	if setCmds[0][16] != 0x04 || setCmds[0][17] != 0x1a {
		t.Errorf("gain value should be 1050, got % x", setCmds[0][16:18])
	}
	// mute payload: [00 01 04 01]
	if setCmds[1][13] != 0x00 || setCmds[1][14] != 0x01 || setCmds[1][15] != 0x04 || setCmds[1][16] != 0x01 {
		t.Errorf("second SET should be mute=true, got % x", setCmds[1][13:17])
	}
	// hpf payload: [00 01 06 01]
	if setCmds[2][13] != 0x00 || setCmds[2][14] != 0x01 || setCmds[2][15] != 0x06 || setCmds[2][16] != 0x01 {
		t.Errorf("third SET should be hpf=1, got % x", setCmds[2][13:17])
	}
	// tone payload: [00 02 04 00 32] (50)
	if setCmds[3][13] != 0x00 || setCmds[3][14] != 0x02 || setCmds[3][15] != 0x04 {
		t.Errorf("fourth SET should be tone, got % x", setCmds[3][13:16])
	}
}

// TestStateCacheNoBlock verifies that sendCached returns immediately even
// before any device state has been fetched.
func TestStateCacheNoBlock(t *testing.T) {
	io := &mockIO{}
	dev := mv7.NewDevice(io)
	srv := New(dev)

	done := make(chan struct{})
	go func() {
		// This must not block even though no state has been fetched yet.
		srv.stateMu.RLock()
		_ = srv.have
		srv.stateMu.RUnlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("state cache access blocked")
	}
}

func TestFailedRefreshInvalidatesCache(t *testing.T) {
	io := &mockIO{}
	srv := New(mv7.NewDevice(io))
	srv.stateMu.Lock()
	srv.state = mv7.DefaultState()
	srv.state.GainDB = 23
	srv.have = true
	srv.connected = true
	srv.stateMu.Unlock()

	srv.refresh()

	srv.stateMu.RLock()
	defer srv.stateMu.RUnlock()
	if srv.have {
		t.Fatal("failed refresh left cached state usable")
	}
	if srv.connected {
		t.Fatal("failed refresh kept connected=true")
	}
	if srv.lastError == "" {
		t.Fatal("failed refresh did not record the error")
	}
}

// fakeIO answers feature and lock GETs like a real MV7+ and can inject
// read failures or missing/truncated values per feature.
type fakeIO struct {
	mu        sync.Mutex
	writes    [][]byte
	lastWrite []byte
	failAll   bool
	skipGain  bool
	skipMute  bool
	shortMute bool
	gainRaw   uint16
	muteRaw   byte
}

func (m *fakeIO) Write(pkt []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, append([]byte(nil), pkt...))
	m.lastWrite = append([]byte(nil), pkt...)
	return nil
}

func (m *fakeIO) writtenPackets() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	packets := make([][]byte, len(m.writes))
	copy(packets, m.writes)
	return packets
}

func (m *fakeIO) ReadTimeout(buf []byte, _ time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAll || m.lastWrite == nil {
		return 0, nil
	}
	pkt := m.lastWrite
	isGetFeat := pkt[10] == 0x01 && pkt[11] == 0x02 && pkt[12] == 0x02
	isGetLock := pkt[10] == 0x01 && pkt[11] == 0x02 && pkt[12] == 0x01
	if !isGetFeat && !isGetLock {
		return 0, nil
	}
	isGain := pkt[14] == 0x01 && pkt[15] == 0x02
	isMute := pkt[14] == 0x01 && pkt[15] == 0x04
	if m.skipGain && isGain {
		return 0, nil
	}
	if m.skipMute && isMute {
		return 0, nil
	}
	var value []byte
	switch {
	case isGain:
		value = []byte{byte(m.gainRaw >> 8), byte(m.gainRaw)}
	case isMute && m.shortMute:
		value = nil
	case isMute:
		value = []byte{m.muteRaw}
	default:
		value = []byte{0, 0, 0, 0}
	}
	cmd := [3]byte{0x03, 0x02, 0x02}
	if isGetLock {
		cmd = [3]byte{0x03, 0x02, 0x01}
	}
	payload := append([]byte{pkt[13], pkt[14], pkt[15]}, value...)
	return copy(buf, buildRespPacket(pkt[4], cmd, payload)), nil
}

func (m *fakeIO) Close() error { return nil }

func buildRespPacket(seq byte, cmd [3]byte, payload []byte) []byte {
	dataLen := byte(3 + len(payload) + 2)
	inner := []byte{0x11, 0x22, seq, 0x03, 0x08, dataLen, 0x70, dataLen}
	inner = append(inner, cmd[:]...)
	inner = append(inner, payload...)
	crc := crc16(inner)
	buf := make([]byte, 64)
	buf[0] = 0x01
	buf[1] = byte(len(inner) + 2)
	copy(buf[2:], inner)
	buf[2+len(inner)] = byte(crc >> 8)
	buf[2+len(inner)+1] = byte(crc & 0xFF)
	return buf
}

// wireEvent mirrors the server→client frame for assertions.
type wireEvent struct {
	Type      string           `json:"type"`
	Connected *bool            `json:"connected,omitempty"`
	Error     string           `json:"error,omitempty"`
	State     *mv7.DeviceState `json:"state,omitempty"`
}

func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func nextWSEvent(t *testing.T, conn *websocket.Conn, within time.Duration) (wireEvent, bool) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(within))
	var ev wireEvent
	if err := conn.ReadJSON(&ev); err != nil {
		return ev, false
	}
	return ev, true
}

func mustStatus(t *testing.T, ev wireEvent, ok bool, wantConnected bool) {
	t.Helper()
	if !ok {
		t.Fatal("no event received")
	}
	if ev.Type != "status" {
		t.Fatalf("event type = %q, want status", ev.Type)
	}
	if ev.Connected == nil || *ev.Connected != wantConnected {
		t.Fatalf("status connected = %v, want %v", ev.Connected, wantConnected)
	}
}

func TestStatusEventPrecedesStateOnConnect(t *testing.T) {
	io := &fakeIO{gainRaw: 2500, muteRaw: 0x01}
	srv := New(mv7.NewDevice(io))
	srv.refresh()

	ts := httptest.NewServer(http.HandlerFunc(srv.HandleWS))
	defer ts.Close()
	conn := dialWS(t, ts)
	defer conn.Close()

	ev, ok := nextWSEvent(t, conn, 2*time.Second)
	mustStatus(t, ev, ok, true)
	ev, ok = nextWSEvent(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("no event received")
	}
	if ev.Type != "state" || ev.State == nil {
		t.Fatalf("second event = %+v, want state", ev)
	}
	if !ev.State.Muted || ev.State.GainDB != 25 {
		t.Fatalf("state = %+v, want muted with gain 25 dB", ev.State)
	}
}

func TestDisconnectedClientGetsNoStaleState(t *testing.T) {
	io := &fakeIO{gainRaw: 2500}
	srv := New(mv7.NewDevice(io))
	srv.refresh() // cache valid
	io.mu.Lock()
	io.failAll = true
	io.mu.Unlock()
	srv.refresh() // must invalidate

	ts := httptest.NewServer(http.HandlerFunc(srv.HandleWS))
	defer ts.Close()
	conn := dialWS(t, ts)
	defer conn.Close()

	ev, ok := nextWSEvent(t, conn, 2*time.Second)
	mustStatus(t, ev, ok, false)
	if ev.Error == "" {
		t.Fatal("status failure should carry an error string")
	}
	if _, ok := nextWSEvent(t, conn, 300*time.Millisecond); ok {
		t.Fatal("stale state was sent to a new client after invalidation")
	}
}

func TestStatusRecoverySendsStatusBeforeState(t *testing.T) {
	io := &fakeIO{gainRaw: 2300, muteRaw: 0x01, failAll: true}
	srv := New(mv7.NewDevice(io))

	ts := httptest.NewServer(http.HandlerFunc(srv.HandleWS))
	defer ts.Close()
	conn := dialWS(t, ts)
	defer conn.Close()

	ev, ok := nextWSEvent(t, conn, 2*time.Second)
	mustStatus(t, ev, ok, false)

	io.mu.Lock()
	io.failAll = false
	io.mu.Unlock()
	srv.refresh()

	ev, ok = nextWSEvent(t, conn, 2*time.Second)
	mustStatus(t, ev, ok, true)
	ev, ok = nextWSEvent(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("no event received")
	}
	if ev.Type != "state" || ev.State == nil || !ev.State.Muted {
		t.Fatalf("recovery state = %+v, want muted state", ev)
	}
}

func TestFailedRefreshEmitsStatusFalseAndNoState(t *testing.T) {
	io := &fakeIO{gainRaw: 2500}
	srv := New(mv7.NewDevice(io))
	srv.refresh()

	ts := httptest.NewServer(http.HandlerFunc(srv.HandleWS))
	defer ts.Close()
	conn := dialWS(t, ts)
	defer conn.Close()
	nextWSEvent(t, conn, 2*time.Second) // status true
	nextWSEvent(t, conn, 2*time.Second) // state

	// Mute getter fails even though gain reads fine: the refresh must be
	// treated as failed and never publish a (possibly default) mute value.
	io.mu.Lock()
	io.skipMute = true
	io.mu.Unlock()
	srv.refresh()

	ev, ok := nextWSEvent(t, conn, 2*time.Second)
	mustStatus(t, ev, ok, false)
	if _, ok := nextWSEvent(t, conn, 300*time.Millisecond); ok {
		t.Fatal("state was emitted after a failed refresh")
	}
}

func TestBroadcastDropsUnresponsiveClient(t *testing.T) {
	srv := New(mv7.NewDevice(&fakeIO{failAll: true}))
	c := &client{send: make(chan wsEvent, 1), done: make(chan struct{})}
	srv.mu.Lock()
	srv.clients[c] = true
	srv.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			srv.broadcast(statusEvent(true, ""))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on an unresponsive client")
	}
	srv.mu.Lock()
	_, present := srv.clients[c]
	srv.mu.Unlock()
	if present {
		t.Fatal("unresponsive client was not removed from the hub")
	}
	select {
	case <-c.done:
	default:
		t.Fatal("unresponsive client was not closed")
	}
}

func TestConcurrentRegisterBroadcastAndWrites(t *testing.T) {
	io := &fakeIO{gainRaw: 2500, muteRaw: 0x01}
	srv := New(mv7.NewDevice(io))
	srv.refresh()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c := &client{send: make(chan wsEvent, sendQueueLen), done: make(chan struct{})}
				srv.register(c)
				srv.unregister(c)
				c.close()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				srv.broadcast(statusEvent(true, ""), wsEvent{Type: "error", Error: "x"})
			}
		}()
	}
	wg.Wait()
}

func TestNoDeviceCommandsOnStartup(t *testing.T) {
	io := &fakeIO{gainRaw: 2500, muteRaw: 0x01}
	srv := New(mv7.NewDevice(io))
	stop := make(chan struct{})
	go srv.Run(50*time.Millisecond, stop)
	defer close(stop)
	time.Sleep(300 * time.Millisecond)

	for i, pkt := range io.writtenPackets() {
		if len(pkt) >= 13 && pkt[10] == 0x02 {
			t.Fatalf("startup write %d is a SET command: % x", i, pkt[10:13])
		}
	}
}

func TestSetGainLockedAction(t *testing.T) {
	io := &mockIO{}
	srv := New(mv7.NewDevice(io))
	msg := wsMessage{
		Action: "set_gain_locked",
		Params: json.RawMessage(`{"value":true}`),
	}

	if err := srv.handleAction(msg); err != nil {
		t.Fatalf("handleAction: %v", err)
	}
	packets := io.writtenPackets()
	if len(packets) != 2 {
		t.Fatalf("writes = %d, want SET and CONFIRM", len(packets))
	}
	if packets[0][14] != 0x01 || packets[0][15] != 0xF3 || packets[0][16] != 1 {
		t.Fatalf("gain-lock payload = % x", packets[0][14:17])
	}
}
