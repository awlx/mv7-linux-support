package server

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

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

func TestFailedRefreshPreservesCachedGain(t *testing.T) {
	io := &mockIO{}
	srv := New(mv7.NewDevice(io))
	srv.state = mv7.DefaultState()
	srv.state.GainDB = 23
	srv.have = true

	srv.refresh()

	srv.stateMu.RLock()
	defer srv.stateMu.RUnlock()
	if !srv.have {
		t.Fatal("failed refresh cleared cached state")
	}
	if srv.state.GainDB != 23 {
		t.Fatalf("gain after failed refresh = %v, want 23", srv.state.GainDB)
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
