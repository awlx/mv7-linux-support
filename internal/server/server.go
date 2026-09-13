// Package server wires the MV7+ device to a WebSocket + HTTP interface.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/awlx/mv7-linux-support/internal/meter"
	"github.com/awlx/mv7-linux-support/internal/mv7"
)

const (
	// writeWait is the per-message deadline for a single client write. A
	// peer that cannot drain its TCP buffer within this window is dropped
	// instead of stalling the device worker.
	writeWait = 5 * time.Second
	// sendQueueLen is the per-client outbound buffer depth.
	sendQueueLen = 64
)

// upgrader upgrades HTTP connections to WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Default same-origin validation prevents unrelated web pages from starting
	// audio capture. Native clients do not send Origin and remain supported.
}

// Server owns the device, a FIFO command queue, a state cache, and the
// WebSocket hub.
//
// A single worker goroutine owns the device exclusively. All device access —
// SET commands and state refreshes — is serialized through the command queue,
// so multiple connected clients can never interleave packets on the wire.
// Clients read state from a cache and never block on the device.
//
// Each client has its own writer goroutine fed from a buffered channel, so
// every frame on a connection (initial snapshot, broadcasts, errors) is
// serialized by exactly one writer and a stalled peer can never block the
// device worker.
type Server struct {
	dev *mv7.Device

	cmds chan wsMessage

	mu      sync.Mutex
	clients map[*client]bool

	stateMu   sync.RWMutex
	state     mv7.DeviceState
	have      bool
	connected bool
	lastError string
	meterWake chan struct{}
	meterRun  func(context.Context, func(meter.Reading))
}

// New creates a Server wrapping the given device.
func New(dev *mv7.Device) *Server {
	return &Server{
		dev:       dev,
		cmds:      make(chan wsMessage, 64),
		clients:   make(map[*client]bool),
		meterWake: make(chan struct{}, 1),
		meterRun:  meter.Run,
	}
}

// wsMessage is a client→server command.
type wsMessage struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// wsEvent is a server→client event.
//
// Backwards-compatible event kinds:
//   - {type:"state", state: DeviceState}  — decoded device state.
//   - {type:"error", error: string}       — command/protocol error.
//   - {type:"status", connected: bool, error?: string} — hardware
//     availability. connected=true means the cached state is backed by a
//     fresh successful read of the hardware (including mute and gain);
//     it is NOT merely the WebSocket link state. Clients must ignore any
//     previously received state while connected=false and must not enable
//     controls until a valid state arrives after a status=true.
type wsEvent struct {
	Type           string           `json:"type"`
	State          *mv7.DeviceState `json:"state,omitempty"`
	Error          string           `json:"error,omitempty"`
	Connected      *bool            `json:"connected,omitempty"`
	MeterSupported *bool            `json:"meter_supported,omitempty"`
	Meter          *meter.Reading   `json:"meter,omitempty"`
}

// statusEvent builds a hardware-availability event.
func statusEvent(connected bool, errMsg string) wsEvent {
	supported := true
	ev := wsEvent{Type: "status", Connected: &connected, MeterSupported: &supported}
	if errMsg != "" {
		ev.Error = errMsg
	}
	return ev
}

// client is one connected WebSocket peer. All writes to its connection go
// through its single writer goroutine.
type client struct {
	conn       *websocket.Conn
	send       chan wsEvent
	done       chan struct{}
	closeOnce  sync.Once
	wantsMeter bool // protected by Server.mu
}

func newClient(conn *websocket.Conn) *client {
	return &client{
		conn: conn,
		send: make(chan wsEvent, sendQueueLen),
		done: make(chan struct{}),
	}
}

// close signals the writer goroutine to stop. Safe to call multiple times.
func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

// submit queues an event for the writer. It never blocks: a full queue or a
// closed client is reported as a failure so the hub can drop the peer.
func (c *client) submit(ev wsEvent) bool {
	select {
	case c.send <- ev:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

// HandleWS upgrades the connection and runs the client loop.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	c := newClient(conn)
	s.register(c)
	defer func() {
		s.unregister(c)
		c.close()
	}()
	s.startWriter(c)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			if !c.submit(wsEvent{Type: "error", Error: "bad message: " + err.Error()}) {
				return
			}
			continue
		}
		if msg.Action == "subscribe_meter" {
			var params struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.Unmarshal(msg.Params, &params); err != nil || params.Enabled == nil {
				if !c.submit(wsEvent{Type: "error", Error: "subscribe_meter requires a boolean enabled parameter"}) {
					return
				}
			} else {
				s.setMeterSubscription(c, *params.Enabled)
			}
			continue
		}
		// Enqueue the command for the device worker. The worker serializes
		// all device access, so concurrent clients cannot interleave packets.
		select {
		case s.cmds <- msg:
		default:
			if !c.submit(wsEvent{Type: "error", Error: "command queue full"}) {
				return
			}
		}
	}
}

// register adds a client and enqueues its initial snapshot while holding the
// hub lock, so the snapshot can never be overtaken by a concurrent broadcast
// (submit is non-blocking, making the under-lock enqueue safe).
func (s *Server) register(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c] = true
	for _, ev := range s.snapshotLocked() {
		if !c.submit(ev) {
			return
		}
	}
}

// unregister removes a client from the hub. Safe to call multiple times.
func (s *Server) unregister(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	s.wakeMeter()
}

// snapshotLocked builds the events a newly connected client must receive to
// be consistent with the current hub state. Called with s.mu held; takes
// s.stateMu inside, matching the worker's lock order (never the reverse).
func (s *Server) snapshotLocked() []wsEvent {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	events := []wsEvent{statusEvent(s.connected, s.lastError)}
	if s.have && s.connected {
		state := s.state
		events = append(events, wsEvent{Type: "state", State: &state})
	}
	return events
}

// startWriter runs the single writer goroutine for a client. Every frame
// written to the connection (snapshot, broadcast, error) is serialized here.
// A failed or timed-out write removes the client so it can never block the
// device worker.
func (s *Server) startWriter(c *client) {
	go func() {
		defer func() {
			s.unregister(c)
			_ = c.conn.Close()
		}()
		for {
			select {
			case ev := <-c.send:
				c.conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := c.conn.WriteJSON(ev); err != nil {
					return
				}
			case <-c.done:
				return
			}
		}
	}()
}

// broadcast queues events to every client in order under the hub lock, so
// per-client frame order is stable, and drops peers whose queue is full or
// closed instead of blocking the caller.
func (s *Server) broadcast(evs ...wsEvent) {
	if len(evs) == 0 {
		return
	}
	s.mu.Lock()
	var dead []*client
	for c := range s.clients {
		for _, ev := range evs {
			if !c.submit(ev) {
				dead = append(dead, c)
				break
			}
		}
	}
	for _, c := range dead {
		delete(s.clients, c)
	}
	s.mu.Unlock()
	for _, c := range dead {
		c.close()
	}
	if len(dead) > 0 {
		s.wakeMeter()
	}
}

// handleAction dispatches a client command to the device.
func (s *Server) handleAction(msg wsMessage) error {
	switch msg.Action {
	case "get_state":
		// State is pushed on connect and via the poller; nothing to do here.
		return nil

	case "set_gain":
		var p struct {
			Value float64 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetGain(p.Value)

	case "set_gain_locked":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetGainLocked(p.Value)

	case "set_mute":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetMute(p.Value)

	case "set_hpf":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetHPF(p.Value)

	case "set_limiter":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLimiter(p.Value)

	case "set_compressor":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetCompressor(p.Value)

	case "set_denoiser":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetDenoiser(p.Value)

	case "set_auto_level":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetAutoLevel(p.Value)

	case "set_mic_mix":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetMicMix(p.Value)

	case "set_playback_mix":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetPlaybackMix(p.Value)

	case "set_popper_stopper":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetPopperStopper(p.Value)

	case "set_tone":
		var p struct {
			Value int16 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetTone(p.Value)

	case "set_mute_btn":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetMuteBtnActive(p.Value)

	case "set_reverb_output":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetReverbOutput(p.Value)

	case "set_reverb_type":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetReverbType(p.Value)

	case "set_reverb_intensity":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetReverbIntensity(p.Value)

	case "set_reverb_monitor":
		var p struct {
			Value bool `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetReverbMonitor(p.Value)

	case "set_led_behavior":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDBehavior(p.Value)

	case "set_led_brightness":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDBrightness(p.Value)

	case "set_led_live_theme":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDLiveTheme(p.Value)

	case "set_led_solid_theme":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDSolidTheme(p.Value)

	case "set_led_pulsing_theme":
		var p struct {
			Value uint8 `json:"value"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDPulsingTheme(p.Value)

	case "set_led_solid_color":
		var p struct {
			RGB [3]uint8 `json:"rgb"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDSolidColor(p.RGB[0], p.RGB[1], p.RGB[2])

	case "set_led_pulsing_color":
		var p struct {
			RGB [3]uint8 `json:"rgb"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDPulsingColor(p.RGB[0], p.RGB[1], p.RGB[2])

	case "set_led_live_edge":
		var p struct {
			RGB [3]uint8 `json:"rgb"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDLiveEdge(p.RGB[0], p.RGB[1], p.RGB[2])

	case "set_led_live_middle":
		var p struct {
			RGB [3]uint8 `json:"rgb"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDLiveMiddle(p.RGB[0], p.RGB[1], p.RGB[2])

	case "set_led_live_interior":
		var p struct {
			RGB [3]uint8 `json:"rgb"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.dev.SetLEDLiveInterior(p.RGB[0], p.RGB[1], p.RGB[2])

	case "factory_reset":
		return s.dev.FactoryReset()

	default:
		return &unknownActionError{action: msg.Action}
	}
}

type unknownActionError struct{ action string }

func (e *unknownActionError) Error() string {
	return "unknown action: " + e.action
}

// Run is the single device worker. It is the only goroutine that touches the
// device, so all commands from all clients are serialized FIFO and can never
// interleave packets on the wire.
//
// It processes queued commands, refreshes state after each command, and also
// refreshes on a fixed interval so the UI stays live. The startup refresh
// only reads (GETs); no SET command is ever sent on startup or reconnect.
func (s *Server) Run(interval time.Duration, stop <-chan struct{}) {
	meterDone := make(chan struct{})
	go func() {
		defer close(meterDone)
		s.runMeter(stop)
	}()
	defer func() { <-meterDone }()
	// Initial refresh.
	s.refresh()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case msg := <-s.cmds:
			if err := s.handleAction(msg); err != nil {
				s.broadcast(wsEvent{Type: "error", Error: err.Error()})
			}
			// Refresh after every command so all clients see the new state.
			s.refresh()
		case <-ticker.C:
			s.refresh()
		}
	}
}

// refresh reads device state and caches + broadcasts it.
//
// On success the cache is updated and clients receive status(connected=true)
// immediately followed by the state, enqueued together under the hub lock.
// On failure the cache is invalidated — a later connection can never receive
// a stale mute value — and clients receive only status(connected=false)
// with the error; no state frame is emitted.
func (s *Server) refresh() {
	state, err := s.dev.GetState()
	if err != nil {
		log.Printf("state refresh failed: %v", err)
		s.stateMu.Lock()
		s.have = false
		s.connected = false
		s.lastError = err.Error()
		s.stateMu.Unlock()
		s.broadcast(statusEvent(false, err.Error()))
		s.wakeMeter()
		return
	}
	s.stateMu.Lock()
	s.state = state
	s.have = true
	s.connected = true
	s.lastError = ""
	s.stateMu.Unlock()
	s.broadcast(statusEvent(true, ""), wsEvent{Type: "state", State: &state})
	s.wakeMeter()
}
