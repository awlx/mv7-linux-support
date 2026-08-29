// Package server wires the MV7+ device to a WebSocket + HTTP interface.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/awlx/mv7-linux-support/internal/mv7"
)

// upgrader upgrades HTTP connections to WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// The UI is served from the same origin (localhost), so no origin check needed.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server owns the device, a FIFO command queue, a state cache, and the
// WebSocket hub.
//
// A single worker goroutine owns the device exclusively. All device access —
// SET commands and state refreshes — is serialized through the command queue,
// so multiple connected clients can never interleave packets on the wire.
// Clients read state from a cache and never block on the device.
type Server struct {
	dev *mv7.Device

	cmds chan wsMessage

	mu      sync.Mutex
	clients map[*websocket.Conn]bool

	stateMu sync.RWMutex
	state   mv7.DeviceState
	have    bool
}

// New creates a Server wrapping the given device.
func New(dev *mv7.Device) *Server {
	return &Server{
		dev:     dev,
		cmds:    make(chan wsMessage, 64),
		clients: make(map[*websocket.Conn]bool),
	}
}

// wsMessage is a client→server command.
type wsMessage struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// wsEvent is a server→client event.
type wsEvent struct {
	Type  string           `json:"type"`
	State *mv7.DeviceState `json:"state,omitempty"`
	Error string           `json:"error,omitempty"`
}

// HandleWS upgrades the connection and runs the client loop.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
	}()

	// Push the cached state on connect (instant, never blocks on the device).
	s.sendCached(conn)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.send(conn, wsEvent{Type: "error", Error: "bad message: " + err.Error()})
			continue
		}
		// Enqueue the command for the device worker. The worker serializes
		// all device access, so concurrent clients cannot interleave packets.
		select {
		case s.cmds <- msg:
		default:
			s.send(conn, wsEvent{Type: "error", Error: "command queue full"})
		}
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

// sendCached sends the latest cached state to a connection.
func (s *Server) sendCached(conn *websocket.Conn) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.have {
		state := s.state
		s.send(conn, wsEvent{Type: "state", State: &state})
	}
}

// send writes an event to a single connection.
func (s *Server) send(conn *websocket.Conn, ev wsEvent) {
	_ = conn.WriteJSON(ev)
}

// broadcast writes an event to all connected clients.
func (s *Server) broadcast(ev wsEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		_ = c.WriteJSON(ev)
	}
}

// Run is the single device worker. It is the only goroutine that touches the
// device, so all commands from all clients are serialized FIFO and can never
// interleave packets on the wire.
//
// It processes queued commands, refreshes state after each command, and also
// refreshes on a fixed interval so the UI stays live.
func (s *Server) Run(interval time.Duration, stop <-chan struct{}) {
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
func (s *Server) refresh() {
	state, err := s.dev.GetState()
	if err != nil {
		log.Printf("state refresh failed: %v", err)
		return
	}
	s.stateMu.Lock()
	s.state = state
	s.have = true
	s.stateMu.Unlock()
	s.broadcast(wsEvent{Type: "state", State: &state})
}
