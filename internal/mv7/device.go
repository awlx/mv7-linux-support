package mv7

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Device is a client for the MV7+ vendor HID console.
type Device struct {
	io        ioDevice
	seq       byte
	mu        sync.Mutex // serializes all packet I/O
	state     DeviceState
	haveState bool
	closed    bool
}

// ioDevice abstracts the raw HID I/O so the client can be tested without
// a real device.
type ioDevice interface {
	Write(pkt []byte) error
	ReadTimeout(buf []byte, timeout time.Duration) (int, error)
	Close() error
}

// NewDevice wraps an ioDevice with the MV7+ protocol client.
func NewDevice(io ioDevice) *Device {
	return &Device{io: io}
}

func (d *Device) nextSeq() byte {
	s := d.seq
	d.seq++
	return s
}

func (d *Device) write(pkt []byte) error {
	if err := d.io.Write(pkt); err != nil {
		return fmt.Errorf("HID write failed: %w", err)
	}
	return nil
}

func (d *Device) read(timeout time.Duration) ([]byte, error) {
	buf := make([]byte, PacketSize)
	n, err := d.io.ReadTimeout(buf, timeout)
	if err != nil {
		return nil, fmt.Errorf("HID read failed: %w", err)
	}
	if n == 0 {
		return nil, errors.New("HID read timed out — no response from device")
	}
	return buf[:n], nil
}

// sendSet writes a SET packet followed by a CONFIRM, then drains the two
// unsolicited IN responses the MV7+ sends after every CONFIRM (a CONFIRM ACK
// and a SET echo) so they don't offset subsequent GET reads.
func (d *Device) sendSet(setPkt []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			setPacketSeq(setPkt, d.nextSeq())
		}
		if err := d.write(setPkt); err != nil {
			return err
		}
		generationAfterSet := deviceGeneration(d.io)
		confirm := cmdConfirm(d.nextSeq())
		if err := d.write(confirm); err != nil {
			return err
		}
		if deviceGeneration(d.io) != generationAfterSet {
			continue
		}
		// Drain the two unsolicited IN responses.
		buf := make([]byte, PacketSize)
		for i := 0; i < 2; i++ {
			_, _ = d.io.ReadTimeout(buf, 50*time.Millisecond)
		}
		return nil
	}
	return errors.New("MV7+ reconnected during SET transaction")
}

func deviceGeneration(io ioDevice) uint64 {
	if device, ok := io.(interface{ Generation() uint64 }); ok {
		return device.Generation()
	}
	return 0
}

// sendGet writes a GET packet and reads the response.
func (d *Device) sendGet(getPkt []byte) (*Response, error) {
	if err := d.write(getPkt); err != nil {
		return nil, err
	}
	expectedPrefix := getPkt[13]
	expectedFeat := [2]byte{getPkt[14], getPkt[15]}
	expectedCommand := resGetFeat
	if getPkt[10] == cmdGetLockCmd[0] && getPkt[11] == cmdGetLockCmd[1] && getPkt[12] == cmdGetLockCmd[2] {
		expectedCommand = resGetLock
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errors.New("HID read timed out — no matching response from device")
		}
		buf, err := d.read(remaining)
		if err != nil {
			return nil, err
		}
		resp, err := ParseResponse(buf)
		if err != nil {
			continue
		}
		if resp.Command == expectedCommand && resp.Prefix == expectedPrefix && resp.Feat == expectedFeat {
			return resp, nil
		}
	}
}

// GetState queries every MV7+ feature and returns the decoded state.
func (d *Device) GetState() (DeviceState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	state := DefaultState()
	if d.haveState {
		state = d.state
	}
	gotGain := false

	getters := []struct {
		name  string
		pkt   []byte
		apply func(*Response, *DeviceState)
	}{
		{"gain", cmdGet(0, 0x00, featGain), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 2 {
				s.GainHundredths = binary.BigEndian.Uint16(r.Value[:2])
				s.GainDB = float64(s.GainHundredths) / 100
				gotGain = true
			}
		}},
		{"gain_lock", cmdGet(0, 0x00, featGainLock), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.GainLocked = r.Value[0] != 0
			}
		}},
		{"mute", cmdGet(0, 0x00, featMute), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.Muted = r.Value[0] != 0
			}
		}},
		{"hpf", cmdGet(0, 0x00, featHPF), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.HPF = r.Value[0]
			}
		}},
		{"limiter", cmdGet(0, 0x00, featLimiter), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.Limiter = r.Value[0] != 0
			}
		}},
		{"compressor", cmdGet(0, 0x00, featCompressor), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.Compressor = r.Value[0]
			}
		}},
		{"denoiser", cmdGet(0, 0x00, featDenoiser), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.Denoiser = r.Value[0] != 0
			}
		}},
		{"auto_level", cmdGet(0, 0x00, featAutoLevel), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.AutoLevel = r.Value[0] != 0
			}
		}},
		{"mic_mix", cmdGet(0, 0x00, featMix), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.MicMix = min(r.Value[0], 100)
			}
		}},
		{"popper_stopper", cmdGet(0, 0x00, featPopper), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.PopperStopper = r.Value[0] != 0
			}
		}},
		{"tone", cmdGet(0, 0x00, featTone), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 2 {
				s.Tone = int16(binary.BigEndian.Uint16(r.Value[:2]))
			}
		}},
		{"mute_btn", cmdGetLock(0, []byte{muteBtnPage, 0x00, muteBtnSub}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				// Inverted: 0x00=disabled, 0x01=active.
				s.MuteBtnActive = r.Value[0] != 0
			}
		}},
		{"reverb_output", cmdGet(0, 0x00, featReverbOut), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.ReverbOutput = r.Value[0] != 0
			}
		}},
		{"reverb_type", cmdGet(0, 0x00, featReverbType), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.ReverbType = r.Value[0]
			}
		}},
		{"reverb_intensity", cmdGet(0, 0x00, featReverbInt), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.ReverbIntensity = min(r.Value[0], 100)
			}
		}},
		{"reverb_monitor", cmdGet(0, 0x00, featReverbMon), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.ReverbMonitor = r.Value[0] != 0
			}
		}},
		{"led_behavior", cmdGetLock(0, []byte{ledPage, 0x00, ledSubBehavior}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.LEDBehavior = r.Value[0]
			}
		}},
		{"led_brightness", cmdGetLock(0, []byte{ledPage, 0x00, ledSubBrightness}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.LEDBrightness = r.Value[0]
			}
		}},
		{"led_live_theme", cmdGetLock(0, []byte{ledPage, 0x00, ledSubLiveTheme}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.LEDLiveTheme = r.Value[0]
			}
		}},
		{"led_live_edge", cmdGetLock(0, []byte{ledPage, 0x00, ledSubLiveEdge}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 4 {
				copy(s.LEDLiveEdge[:], r.Value[1:4])
			}
		}},
		{"led_live_middle", cmdGetLock(0, []byte{ledPage, 0x00, ledSubLiveMid}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 4 {
				copy(s.LEDLiveMiddle[:], r.Value[1:4])
			}
		}},
		{"led_live_interior", cmdGetLock(0, []byte{ledPage, 0x00, ledSubLiveInt}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 4 {
				copy(s.LEDLiveInterior[:], r.Value[1:4])
			}
		}},
		{"led_solid_color", cmdGetLock(0, []byte{ledPage, 0x00, ledSubSolidColor}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 4 {
				copy(s.LEDSolidColor[:], r.Value[1:4])
			}
		}},
		{"led_pulsing_color", cmdGetLock(0, []byte{ledPage, 0x00, ledSubPulseColor}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 4 {
				copy(s.LEDPulsingColor[:], r.Value[1:4])
			}
		}},
		{"led_solid_theme", cmdGetLock(0, []byte{ledPage, 0x00, ledSubSolidTheme}), func(r *Response, s *DeviceState) {
			if len(r.Value) >= 1 {
				s.LEDSolidTheme = r.Value[0]
			}
		}},
		{"device_name", cmdGetLock(0, []byte{0x00, featDevName[0], featDevName[1]}), func(r *Response, s *DeviceState) {
			if v := decodeLPString(r.Value); v != "" {
				s.DeviceName = v
			}
		}},
		{"firmware", cmdGetLock(0, []byte{0x00, featFirmware[0], featFirmware[1]}), func(r *Response, s *DeviceState) {
			if v := decodeLPString(r.Value); v != "" {
				s.Firmware = v
			}
		}},
		{"serial", cmdGetLock(0, []byte{0x00, featSerial[0], featSerial[1]}), func(r *Response, s *DeviceState) {
			if v := decodeLPString(r.Value); v != "" {
				s.Serial = v
			}
		}},
	}

	successfulGets := 0
	for _, g := range getters {
		pkt := g.pkt
		setPacketSeq(pkt, d.nextSeq())
		resp, err := d.sendGet(pkt)
		if err != nil {
			continue // skip features that don't respond
		}
		successfulGets++
		g.apply(resp, &state)
	}

	// Playback mix shares FEAT_MIX with mic mix but uses prefix=0x03.
	pmixPkt := cmdGet(0, 0x03, featMix)
	setPacketSeq(pmixPkt, d.nextSeq())
	if resp, err := d.sendGet(pmixPkt); err == nil && len(resp.Value) >= 1 {
		successfulGets++
		state.PlaybackMix = min(resp.Value[0], 100)
	}
	if successfulGets == 0 {
		return DeviceState{}, errors.New("MV7+ did not respond to any state queries")
	}
	if !gotGain {
		return DeviceState{}, errors.New("MV7+ did not return its current gain")
	}
	d.state = state
	d.haveState = true

	return state, nil
}

// decodeLPString decodes a length-prefixed ASCII string as returned by the
// lock-class identity features (device name, firmware, serial). The value is
// [len_hi, len_lo, bytes...] where len is a big-endian u16 equal to the full
// value length.
func decodeLPString(value []byte) string {
	if len(value) < 3 {
		return ""
	}
	declared := int(binary.BigEndian.Uint16(value[:2]))
	if declared != len(value) {
		return ""
	}
	return string(value[2:])
}

// SetGain sets manual gain in 0.5 dB increments (0–36).
func (d *Device) SetGain(gainDB float64) error {
	if gainDB < 0 {
		gainDB = 0
	}
	if gainDB > 36 {
		gainDB = 36
	}
	gainDB = math.Round(gainDB*2) / 2
	raw := make([]byte, 2)
	binary.BigEndian.PutUint16(raw, uint16(math.Round(gainDB*100)))
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setLocked(cmdSet(0, featGain, raw))
}

// SetGainLocked prevents or allows manual gain changes.
func (d *Device) SetGainLocked(locked bool) error {
	return d.set(cmdSet(0, featGainLock, boolByte(locked)))
}

// SetMute mutes or unmutes the microphone.
func (d *Device) SetMute(muted bool) error {
	return d.set(cmdSet(0, featMute, boolByte(muted)))
}

// SetHPF sets the high-pass filter: 0=off, 1=75Hz, 2=150Hz.
func (d *Device) SetHPF(hpf uint8) error {
	if hpf > 2 {
		hpf = 2
	}
	return d.set(cmdSet(0, featHPF, []byte{hpf}))
}

// SetLimiter enables or disables the limiter.
func (d *Device) SetLimiter(enabled bool) error {
	return d.set(cmdSet(0, featLimiter, boolByte(enabled)))
}

// SetCompressor sets the compressor preset: 0=off, 1=light, 2=medium, 3=heavy.
func (d *Device) SetCompressor(preset uint8) error {
	if preset > 3 {
		preset = 3
	}
	return d.set(cmdSet(0, featCompressor, []byte{preset}))
}

// SetDenoiser enables or disables the denoiser.
func (d *Device) SetDenoiser(enabled bool) error {
	return d.set(cmdSet(0, featDenoiser, boolByte(enabled)))
}

// SetAutoLevel switches between Auto Level (true) and Manual (false).
func (d *Device) SetAutoLevel(auto bool) error {
	return d.set(cmdSet(0, featAutoLevel, boolByte(auto)))
}

// SetMicMix sets the mic monitor mix (0=full mic, 100=full playback).
func (d *Device) SetMicMix(mix uint8) error {
	if mix > 100 {
		mix = 100
	}
	return d.set(cmdSetPrefix(0, 0x00, featMix, []byte{mix}))
}

// SetPlaybackMix sets the playback mix (0=full mic, 100=full playback).
func (d *Device) SetPlaybackMix(mix uint8) error {
	if mix > 100 {
		mix = 100
	}
	return d.set(cmdSetPrefix(0, 0x03, featMix, []byte{mix}))
}

// SetPopperStopper enables or disables the popper stopper.
func (d *Device) SetPopperStopper(enabled bool) error {
	return d.set(cmdSet(0, featPopper, boolByte(enabled)))
}

// SetTone sets the tone character (-100..+100 percent).
func (d *Device) SetTone(tone int16) error {
	if tone < -100 {
		tone = -100
	}
	if tone > 100 {
		tone = 100
	}
	raw := make([]byte, 2)
	binary.BigEndian.PutUint16(raw, uint16(int16(tone)))
	return d.set(cmdSet(0, featTone, raw))
}

// SetMuteBtnActive sets whether the physical mute button is active.
// (Inverted encoding: 0x00=disabled, 0x01=active.)
func (d *Device) SetMuteBtnActive(active bool) error {
	enableByte := byte(0x01)
	if !active {
		enableByte = 0x00
	}
	payload := []byte{muteBtnPage, 0x00, muteBtnSub, enableByte}
	return d.set(cmdSetLockHdr0(0, payload))
}

// SetReverbOutput enables or disables reverb on the speaker/headphone output.
func (d *Device) SetReverbOutput(enabled bool) error {
	return d.set(cmdSet(0, featReverbOut, boolByte(enabled)))
}

// SetReverbType sets the reverb type: 0=Plate, 1=Hall, 2=Studio.
func (d *Device) SetReverbType(rtype uint8) error {
	if rtype > 2 {
		rtype = 2
	}
	return d.set(cmdSet(0, featReverbType, []byte{rtype}))
}

// SetReverbIntensity sets the reverb intensity (0–100).
func (d *Device) SetReverbIntensity(intensity uint8) error {
	if intensity > 100 {
		intensity = 100
	}
	return d.set(cmdSet(0, featReverbInt, []byte{intensity}))
}

// SetReverbMonitor enables or disables reverb on direct monitoring.
func (d *Device) SetReverbMonitor(enabled bool) error {
	return d.set(cmdSet(0, featReverbMon, boolByte(enabled)))
}

// SetLEDBehavior sets the LED behavior: 0=Live, 1=Pulsing, 2=Solid.
func (d *Device) SetLEDBehavior(behavior uint8) error {
	if behavior > 2 {
		behavior = 2
	}
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubBehavior, behavior}))
}

// SetLEDBrightness sets the LED brightness: 3=Low, 4=Med, 5=High, 6=Max.
func (d *Device) SetLEDBrightness(brightness uint8) error {
	if brightness < 3 || brightness > 6 {
		return fmt.Errorf("invalid LED brightness %d", brightness)
	}
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubBrightness, brightness}))
}

// SetLEDLiveTheme sets the Live color theme: 0=Default, 1=Seaside, 2=Space,
// 3=Fruity, 4=Custom.
func (d *Device) SetLEDLiveTheme(theme uint8) error {
	if theme > 4 {
		theme = 4
	}
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubLiveTheme, theme}))
}

// SetLEDSolidTheme sets the Solid theme: 0=Shure, 1=Custom.
func (d *Device) SetLEDSolidTheme(theme uint8) error {
	if theme > 1 {
		theme = 1
	}
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubSolidTheme, theme}))
}

// SetLEDPulsingTheme sets the Pulsing theme: 0=Shure, 1=Custom.
func (d *Device) SetLEDPulsingTheme(theme uint8) error {
	if theme > 1 {
		theme = 1
	}
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubPulseTheme, theme}))
}

// SetLEDSolidColor sets the custom Solid LED color.
func (d *Device) SetLEDSolidColor(r, g, b uint8) error {
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubSolidColor, 0x00, r, g, b}))
}

// SetLEDPulsingColor sets the custom Pulsing LED color.
func (d *Device) SetLEDPulsingColor(r, g, b uint8) error {
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubPulseColor, 0x00, r, g, b}))
}

// SetLEDLiveEdge sets the Live Custom edge zone color.
func (d *Device) SetLEDLiveEdge(r, g, b uint8) error {
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubLiveEdge, 0x00, r, g, b}))
}

// SetLEDLiveMiddle sets the Live Custom middle zone color.
func (d *Device) SetLEDLiveMiddle(r, g, b uint8) error {
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubLiveMid, 0x00, r, g, b}))
}

// SetLEDLiveInterior sets the Live Custom interior zone color.
func (d *Device) SetLEDLiveInterior(r, g, b uint8) error {
	return d.set(cmdSetLockHdr0(0, []byte{ledPage, 0x00, ledSubLiveInt, 0x00, r, g, b}))
}

// FactoryReset resets the device to factory defaults. The device disconnects
// and re-enumerates immediately; no CONFIRM is sent.
func (d *Device) FactoryReset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	pkt := cmdSetLockHdr0(d.nextSeq(), []byte{0x00, 0x00, 0x22, 0x01})
	return d.write(pkt)
}

// set serializes a SET + CONFIRM exchange.
func (d *Device) set(pkt []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setLocked(pkt)
}

func (d *Device) setLocked(pkt []byte) error {
	setPacketSeq(pkt, d.nextSeq())
	return d.sendSet(pkt)
}

// Close closes the underlying device.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.io.Close()
}

func boolByte(b bool) []byte {
	if b {
		return []byte{0x01}
	}
	return []byte{0x00}
}
