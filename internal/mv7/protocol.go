// Package mv7 implements the Shure MV7+ USB HID protocol (Shure DMP over HID ADT).
//
// The protocol is a binary packet protocol carried over the vendor HID interface
// (report ID 0x01, 64-byte reports). Every packet is CRC-16/ANSI protected and
// every SET command must be followed by a CONFIRM packet.
//
// Reference: https://github.com/Humblemonk/shurectl (src/protocol.rs), which
// documents the protocol for the MVX2U, MV6 and MV7+ (VID 0x14ED, PID 0x1019).
package mv7

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// PacketSize is the fixed HID report size in bytes.
const PacketSize = 64

// Fixed header bytes.
const (
	reportID    = 0x01
	headerMagic = 0x1122 // [0x11, 0x22]
	hdrEnd      = 0x08
	dataStart   = 0x70
)

// Command bytes (3 bytes each).
var (
	cmdGetFeat    = [3]byte{0x01, 0x02, 0x02}
	cmdSetFeat    = [3]byte{0x02, 0x02, 0x02}
	cmdConfirmCmd = [3]byte{0x01, 0x00, 0x00}
	cmdGetLockCmd = [3]byte{0x01, 0x02, 0x01}
	cmdSetLockCmd = [3]byte{0x02, 0x02, 0x01}
	resGetFeat    = [3]byte{0x03, 0x02, 0x02}
	resSetFeat    = [3]byte{0x04, 0x02, 0x02}
	resGetLock    = [3]byte{0x03, 0x02, 0x01}
	resSetLock    = [3]byte{0x04, 0x02, 0x01}
	resConfirmAck = [3]byte{0x09, 0x00, 0x00}
)

// Feature addresses (2 bytes each).
var (
	featGain       = [2]byte{0x01, 0x02}
	featMute       = [2]byte{0x01, 0x04}
	featHPF        = [2]byte{0x01, 0x06}
	featLimiter    = [2]byte{0x01, 0x51}
	featCompressor = [2]byte{0x01, 0x5C}
	featDenoiser   = [2]byte{0x01, 0x58}
	featAutoLevel  = [2]byte{0x01, 0x85}
	featMix        = [2]byte{0x01, 0x86}
	featPopper     = [2]byte{0x03, 0x81}
	featTone       = [2]byte{0x02, 0x04}
	featGainLock   = [2]byte{0x01, 0xF3}
	featReverbOut  = [2]byte{0x03, 0x82}
	featReverbType = [2]byte{0x03, 0x83}
	featReverbInt  = [2]byte{0x03, 0x84}
	featReverbMon  = [2]byte{0x03, 0x85}
	featDevName    = [2]byte{0x00, 0x12}
	featFirmware   = [2]byte{0x00, 0x09}
	featSerial     = [2]byte{0x00, 0x02}
)

// MV7+ LED page and sub-addresses (3-byte address: [0x0C, 0x00, sub]).
const (
	ledPage          = 0x0C
	ledSubBrightness = 0x42
	ledSubLiveTheme  = 0x96
	ledSubLiveEdge   = 0x98
	ledSubLiveMid    = 0x99
	ledSubLiveInt    = 0xA0
	ledSubSolidColor = 0xA2
	ledSubPulseColor = 0xA3
	ledSubBehavior   = 0xA4
	ledSubSolidTheme = 0xA5
	ledSubPulseTheme = 0xA6
)

// Mute button disable uses a 3-byte address [0x0C, 0x00, 0x60].
const (
	muteBtnPage = 0x0C
	muteBtnSub  = 0x60
)

// crc16ANSI computes CRC-16/ANSI (poly 0x8005, reflected 0xA001, init 0x0000).
func crc16ANSI(data []byte) uint16 {
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

// buildPacket assembles a 64-byte packet. hdrConst is 0x03 for GETs and 0x00
// for all MV7+ SET commands.
func buildPacket(seq byte, cmd [3]byte, payload []byte, hdrConst byte) []byte {
	dataLen := byte(3 + len(payload) + 2)

	inner := make([]byte, 0, PacketSize)
	inner = append(inner, 0x11, 0x22)
	inner = append(inner, seq, hdrConst, hdrEnd)
	inner = append(inner, dataLen, dataStart, dataLen)
	inner = append(inner, cmd[:]...)
	inner = append(inner, payload...)

	totalLen := byte(len(inner) + 2)
	crc := crc16ANSI(inner)

	pkt := make([]byte, PacketSize)
	pkt[0] = reportID
	pkt[1] = totalLen
	copy(pkt[2:], inner)
	pkt[2+len(inner)] = byte(crc >> 8)
	pkt[2+len(inner)+1] = byte(crc & 0xFF)
	return pkt
}

// buildPacketHdr0 builds a packet with HDR_CONSTANT=0x00 (MV7+ SET commands).
func buildPacketHdr0(seq byte, cmd [3]byte, payload []byte) []byte {
	return buildPacket(seq, cmd, payload, 0x00)
}

// setPacketSeq updates a built packet's sequence number and CRC.
func setPacketSeq(pkt []byte, seq byte) {
	pkt[4] = seq
	contentsEnd := int(pkt[1])
	crc := crc16ANSI(pkt[2:contentsEnd])
	pkt[contentsEnd] = byte(crc >> 8)
	pkt[contentsEnd+1] = byte(crc)
}

// cmdGet builds a GET packet for a feature with the given prefix byte.
func cmdGet(seq byte, prefix byte, feat [2]byte) []byte {
	return buildPacket(seq, cmdGetFeat, []byte{prefix, feat[0], feat[1]}, 0x03)
}

// cmdSet builds a standard MV7+ SET packet (HDR_CONSTANT=0x00, prefix=0x00).
func cmdSet(seq byte, feat [2]byte, value []byte) []byte {
	payload := []byte{0x00, feat[0], feat[1]}
	payload = append(payload, value...)
	return buildPacketHdr0(seq, cmdSetFeat, payload)
}

// cmdSetPrefix builds an MV7+ SET packet with an explicit prefix byte
// (used for mic mix prefix=0x00 and playback mix prefix=0x03).
func cmdSetPrefix(seq byte, prefix byte, feat [2]byte, value []byte) []byte {
	payload := []byte{prefix, feat[0], feat[1]}
	payload = append(payload, value...)
	return buildPacketHdr0(seq, cmdSetFeat, payload)
}

// cmdConfirm builds a CONFIRM packet (must follow every SET).
func cmdConfirm(seq byte) []byte {
	return buildPacket(seq, cmdConfirmCmd, nil, 0x03)
}

// cmdGetLock builds a GET packet in the lock command class.
func cmdGetLock(seq byte, payload []byte) []byte {
	return buildPacket(seq, cmdGetLockCmd, payload, 0x03)
}

// cmdSetLockHdr0 builds a SET packet in the lock command class with
// HDR_CONSTANT=0x00 (used for LED features and mute button disable).
func cmdSetLockHdr0(seq byte, payload []byte) []byte {
	return buildPacketHdr0(seq, cmdSetLockCmd, payload)
}

// Response is a decoded device response.
type Response struct {
	Command [3]byte
	Prefix  byte
	Feat    [2]byte
	Value   []byte
}

// ParseResponse validates a received HID report and extracts the feature
// address and value bytes. Returns an error if the buffer is malformed, the
// header magic is wrong, or the CRC does not match.
func ParseResponse(buf []byte) (*Response, error) {
	// Minimum: report_id(1) + total_len(1) + header(2) + seq(1) + hdr_const(1) +
	// hdr_end(1) + data_len(1) + data_start(1) + data_len(1) + cmd(3) +
	// prefix(1) + feat(2) + crc(2) = 18 bytes.
	if len(buf) < 18 {
		return nil, errors.New("response too short")
	}
	contentsEnd := int(buf[1])
	if contentsEnd+2 > len(buf) {
		return nil, errors.New("response total_len out of range")
	}
	if buf[2] != 0x11 || buf[3] != 0x22 {
		return nil, errors.New("bad header magic")
	}
	expected := binary.BigEndian.Uint16(buf[contentsEnd : contentsEnd+2])
	actual := crc16ANSI(buf[2:contentsEnd])
	if actual != expected {
		return nil, fmt.Errorf("CRC mismatch: got %04x want %04x", actual, expected)
	}

	var respType [3]byte
	copy(respType[:], buf[10:13])
	switch respType {
	case resGetFeat, resSetFeat, resGetLock, resSetLock:
	default:
		// CONFIRM ACK and other responses carry no feature data.
		return nil, errors.New("response carries no feature data")
	}

	if len(buf) < 16 {
		return nil, errors.New("response too short for feature data")
	}
	return &Response{
		Command: respType,
		Prefix:  buf[13],
		Feat:    [2]byte{buf[14], buf[15]},
		Value:   append([]byte(nil), buf[16:contentsEnd]...),
	}, nil
}

// IsConfirmAck reports whether the buffer is the unsolicited CONFIRM ACK
// (cmd [09 00 00]) that the MV7+ sends after every CONFIRM.
func IsConfirmAck(buf []byte) bool {
	if len(buf) < 13 {
		return false
	}
	return buf[10] == resConfirmAck[0] && buf[11] == resConfirmAck[1] && buf[12] == resConfirmAck[2]
}
