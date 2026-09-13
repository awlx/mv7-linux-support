package mv7

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

type generationIO struct {
	generation       uint64
	reconnectOnWrite int
	writes           [][]byte
}

func (m *generationIO) Write(pkt []byte) error {
	m.writes = append(m.writes, append([]byte(nil), pkt...))
	if len(m.writes) == m.reconnectOnWrite {
		m.generation++
	}
	return nil
}

func (m *generationIO) ReadTimeout([]byte, time.Duration) (int, error) {
	return 0, nil
}

func (m *generationIO) Close() error { return nil }

func (m *generationIO) Generation() uint64 { return m.generation }

type startupIO struct {
	lastWrite  []byte
	writes     [][]byte
	gainRaw    uint16
	gainLocked bool
	generation uint64
	skipGain   bool
	skipMute   bool
	shortMute  bool
	muteRaw    byte
}

func (m *startupIO) Write(pkt []byte) error {
	m.lastWrite = append([]byte(nil), pkt...)
	m.writes = append(m.writes, m.lastWrite)
	return nil
}

func (m *startupIO) ReadTimeout(buf []byte, _ time.Duration) (int, error) {
	isGain := m.lastWrite[14] == featGain[0] && m.lastWrite[15] == featGain[1]
	isMute := m.lastWrite[14] == featMute[0] && m.lastWrite[15] == featMute[1]
	if m.skipGain && isGain {
		return 0, nil
	}
	if m.skipMute && isMute {
		return 0, nil
	}
	cmd := resGetFeat
	if m.lastWrite[12] == cmdGetLockCmd[2] {
		cmd = resGetLock
	}
	value := []byte{0, 0, 0, 0}
	if isGain {
		value = []byte{byte(m.gainRaw >> 8), byte(m.gainRaw)}
	} else if m.lastWrite[14] == featGainLock[0] && m.lastWrite[15] == featGainLock[1] {
		value = boolByte(m.gainLocked)
	} else if isMute {
		if m.shortMute {
			value = nil
		} else {
			value = []byte{m.muteRaw}
		}
	}
	payload := []byte{m.lastWrite[13], m.lastWrite[14], m.lastWrite[15]}
	payload = append(payload, value...)
	response := buildPacket(m.lastWrite[4], cmd, payload, 0x03)
	return copy(buf, response), nil
}

func (m *startupIO) Close() error { return nil }

func (m *startupIO) Generation() uint64 { return m.generation }

type responseQueueIO struct {
	responses [][]byte
}

func (m *responseQueueIO) Write([]byte) error { return nil }

func (m *responseQueueIO) ReadTimeout(buf []byte, _ time.Duration) (int, error) {
	if len(m.responses) == 0 {
		return 0, errors.New("no response")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return copy(buf, response), nil
}

func (m *responseQueueIO) Close() error { return nil }

func gainResponse(seq byte, gainDB uint16) []byte {
	raw := gainDB * 100
	return buildPacket(seq, resGetFeat, []byte{
		0x00, featGain[0], featGain[1], byte(raw >> 8), byte(raw),
	}, 0x03)
}

func TestSendGetIgnoresStaleGainSetEcho(t *testing.T) {
	io := &responseQueueIO{responses: [][]byte{
		buildPacket(6, resSetFeat, []byte{0x00, featGain[0], featGain[1], 0x00, 0x00}, 0x03),
		gainResponse(42, 23),
	}}
	dev := NewDevice(io)
	response, err := dev.sendGet(cmdGet(7, 0x00, featGain))
	if err != nil {
		t.Fatalf("sendGet: %v", err)
	}
	gain := binary.BigEndian.Uint16(response.Value[:2]) / 100
	if gain != 23 {
		t.Fatalf("gain = %d, want current response value 23", gain)
	}
}

func TestGetStateDoesNotSetGainOnStartup(t *testing.T) {
	io := &startupIO{gainRaw: 2300}
	dev := NewDevice(io)

	state, err := dev.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state.GainDB != 23 {
		t.Fatalf("gain = %v, want 23", state.GainDB)
	}
	if state.GainHundredths != 2300 {
		t.Fatalf("raw gain = %d, want 2300", state.GainHundredths)
	}
	for index, pkt := range io.writes {
		if pkt[10] == cmdSetFeat[0] || pkt[10] == cmdSetLockCmd[0] {
			t.Fatalf("startup write %d is SET command % x", index, pkt[10:13])
		}
	}
}

func TestGetStateReportsLatestDeviceGainWithoutSettingIt(t *testing.T) {
	io := &startupIO{gainRaw: 2300}
	dev := NewDevice(io)
	if _, err := dev.GetState(); err != nil {
		t.Fatalf("first GetState: %v", err)
	}

	io.gainRaw = 1700
	io.generation++
	io.writes = nil
	state, err := dev.GetState()
	if err != nil {
		t.Fatalf("second GetState: %v", err)
	}
	if state.GainDB != 17 {
		t.Fatalf("gain = %v, want latest device value 17", state.GainDB)
	}
	for index, pkt := range io.writes {
		if pkt[10] == cmdSetFeat[0] || pkt[10] == cmdSetLockCmd[0] {
			t.Fatalf("readback write %d is SET command % x", index, pkt[10:13])
		}
	}
}

func TestGetStateReportsHalfDecibelGain(t *testing.T) {
	io := &startupIO{gainRaw: 1050}
	dev := NewDevice(io)

	state, err := dev.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state.GainDB != 10.5 {
		t.Fatalf("gain = %v, want 10.5", state.GainDB)
	}
}

func TestSetGainEncodesHalfDecibel(t *testing.T) {
	io := &generationIO{}
	dev := NewDevice(io)

	if err := dev.SetGain(10.5); err != nil {
		t.Fatalf("SetGain: %v", err)
	}
	if len(io.writes) < 1 {
		t.Fatal("SetGain wrote no packets")
	}
	if got := binary.BigEndian.Uint16(io.writes[0][16:18]); got != 1050 {
		t.Fatalf("raw gain = %d, want 1050", got)
	}
}

func TestGetStateRejectsRefreshWithoutCurrentGain(t *testing.T) {
	io := &startupIO{gainRaw: 2300}
	dev := NewDevice(io)
	if _, err := dev.GetState(); err != nil {
		t.Fatalf("first GetState: %v", err)
	}

	io.generation++
	io.skipGain = true
	if _, err := dev.GetState(); err == nil {
		t.Fatal("GetState succeeded without a current gain response")
	}
}

func TestGetStateRejectsRefreshWithoutMuteRead(t *testing.T) {
	io := &startupIO{gainRaw: 2300, skipMute: true}
	dev := NewDevice(io)

	if _, err := dev.GetState(); err == nil || !strings.Contains(err.Error(), "mute") {
		t.Fatalf("GetState error = %v, want a mute read failure even though gain succeeded", err)
	}
}

func TestGetStateRejectsTruncatedMuteResponse(t *testing.T) {
	io := &startupIO{gainRaw: 2300, shortMute: true}
	dev := NewDevice(io)

	if _, err := dev.GetState(); err == nil || !strings.Contains(err.Error(), "mute") {
		t.Fatalf("GetState error = %v, want a mute read failure for a valueless response", err)
	}
}

func TestGetStateReadsMuteState(t *testing.T) {
	io := &startupIO{gainRaw: 1500, muteRaw: 0x01}
	dev := NewDevice(io)

	state, err := dev.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !state.Muted {
		t.Fatal("mute state was not read from the device")
	}
}

func TestSendSetReplaysTransactionWhenConfirmReconnects(t *testing.T) {
	io := &generationIO{generation: 1, reconnectOnWrite: 2}
	dev := NewDevice(io)
	pkt := cmdSet(0, featGain, []byte{0x08, 0xfc})

	if err := dev.sendSet(pkt); err != nil {
		t.Fatalf("sendSet: %v", err)
	}
	if len(io.writes) != 4 {
		t.Fatalf("writes = %d, want SET+CONFIRM replayed once", len(io.writes))
	}
	for _, index := range []int{0, 2} {
		if io.writes[index][10] != 0x02 {
			t.Fatalf("write %d is not SET", index)
		}
	}
	for _, index := range []int{1, 3} {
		if io.writes[index][10] != 0x01 || io.writes[index][11] != 0x00 {
			t.Fatalf("write %d is not CONFIRM", index)
		}
	}
}

func TestSetGainLockedUsesGainLockFeature(t *testing.T) {
	for _, locked := range []bool{true, false} {
		io := &generationIO{}
		dev := NewDevice(io)

		if err := dev.SetGainLocked(locked); err != nil {
			t.Fatalf("SetGainLocked(%t): %v", locked, err)
		}
		if len(io.writes) != 2 {
			t.Fatalf("writes = %d, want SET and CONFIRM", len(io.writes))
		}
		pkt := io.writes[0]
		if pkt[5] != 0x00 {
			t.Fatalf("header constant = %02x, want 00", pkt[5])
		}
		if pkt[10] != cmdSetFeat[0] || pkt[11] != cmdSetFeat[1] || pkt[12] != cmdSetFeat[2] {
			t.Fatalf("command = % x, want SET_FEAT", pkt[10:13])
		}
		if pkt[14] != featGainLock[0] || pkt[15] != featGainLock[1] || pkt[16] != boolByte(locked)[0] {
			t.Fatalf("payload = % x, want gain lock %t", pkt[14:17], locked)
		}
	}
}

func TestGetStateReadsGainLock(t *testing.T) {
	io := &startupIO{gainRaw: 1500, gainLocked: true}
	dev := NewDevice(io)

	state, err := dev.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !state.GainLocked {
		t.Fatal("gain lock was not read from the device")
	}
}
