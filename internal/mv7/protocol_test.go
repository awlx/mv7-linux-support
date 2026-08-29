package mv7

import (
	"bytes"
	"testing"
)

// TestCRC16ANSI verifies the CRC-16/ANSI implementation against known values.
func TestCRC16ANSI(t *testing.T) {
	// CRC-16/ANSI (poly 0x8005 reflected) of "123456789" is 0xBB3D.
	got := crc16ANSI([]byte("123456789"))
	if got != 0xBB3D {
		t.Fatalf("crc16_ansi(\"123456789\") = %04x, want bb3d", got)
	}
}

func TestSetPacketSeqRecalculatesCRC(t *testing.T) {
	pkt := cmdGet(0, 0x00, featGain)
	setPacketSeq(pkt, 0x42)

	if pkt[4] != 0x42 {
		t.Fatalf("sequence = %02x, want 42", pkt[4])
	}
	totalLen := int(pkt[1])
	got := uint16(pkt[totalLen])<<8 | uint16(pkt[totalLen+1])
	want := crc16ANSI(pkt[2:totalLen])
	if got != want {
		t.Fatalf("CRC after sequence update = %04x, want %04x", got, want)
	}
}

// TestBuildPacket verifies the packet layout matches the documented structure.
func TestBuildPacket(t *testing.T) {
	// GET gain: seq=0, cmd=[01 02 02], payload=[00 01 02]
	pkt := cmdGet(0, 0x00, featGain)

	if len(pkt) != PacketSize {
		t.Fatalf("packet length = %d, want %d", len(pkt), PacketSize)
	}
	if pkt[0] != 0x01 {
		t.Errorf("report ID = %02x, want 01", pkt[0])
	}
	if pkt[2] != 0x11 || pkt[3] != 0x22 {
		t.Errorf("header magic = %02x %02x, want 11 22", pkt[2], pkt[3])
	}
	if pkt[4] != 0x00 {
		t.Errorf("seq = %02x, want 00", pkt[4])
	}
	if pkt[5] != 0x03 {
		t.Errorf("hdr const = %02x, want 03 (GET)", pkt[5])
	}
	if pkt[7] != 0x08 {
		t.Errorf("hdr end = %02x, want 08", pkt[7])
	}
	if pkt[8] != 0x70 {
		t.Errorf("data start = %02x, want 70", pkt[8])
	}
	// cmd at [10..13]
	if !bytes.Equal(pkt[10:13], []byte{0x01, 0x02, 0x02}) {
		t.Errorf("cmd = % x, want 01 02 02", pkt[10:13])
	}
	// payload prefix + feat addr at [13..16]
	if !bytes.Equal(pkt[13:16], []byte{0x00, 0x01, 0x02}) {
		t.Errorf("payload = % x, want 00 01 02", pkt[13:16])
	}

	// Verify CRC covers bytes [2..total_len).
	totalLen := int(pkt[1])
	expected := crc16ANSI(pkt[2:totalLen])
	got := uint16(pkt[totalLen])<<8 | uint16(pkt[totalLen+1])
	if got != expected {
		t.Errorf("CRC = %04x, want %04x", got, expected)
	}
}

// TestBuildPacketHdr0 verifies MV7+ SET packets use HDR_CONSTANT=0x00.
func TestBuildPacketHdr0(t *testing.T) {
	pkt := cmdSet(0, featMute, []byte{0x01})
	if pkt[5] != 0x00 {
		t.Errorf("hdr const = %02x, want 00 (MV7+ SET)", pkt[5])
	}
	if !bytes.Equal(pkt[10:13], []byte{0x02, 0x02, 0x02}) {
		t.Errorf("cmd = % x, want 02 02 02", pkt[10:13])
	}
	if !bytes.Equal(pkt[13:17], []byte{0x00, 0x01, 0x04, 0x01}) {
		t.Errorf("payload = % x, want 00 01 04 01", pkt[13:17])
	}
}

// TestParseResponseRoundTrip builds a response buffer and verifies parsing.
func TestParseResponseRoundTrip(t *testing.T) {
	// Build a fake response: RES_GET_FEAT [03 02 02], prefix 0x00,
	// feat [01 02], value [0x0F, 0xA0] (gain 40.00 dB → 4000).
	inner := []byte{0x11, 0x22, 0x01, 0x03, 0x08, 0x09, 0x70, 0x09,
		0x03, 0x02, 0x02, 0x00, 0x01, 0x02, 0x0F, 0xA0}
	crc := crc16ANSI(inner)
	buf := make([]byte, PacketSize)
	buf[0] = 0x01
	buf[1] = byte(len(inner) + 2)
	copy(buf[2:], inner)
	buf[2+len(inner)] = byte(crc >> 8)
	buf[2+len(inner)+1] = byte(crc & 0xFF)

	resp, err := ParseResponse(buf)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Feat != [2]byte{0x01, 0x02} {
		t.Errorf("feat = % x, want 01 02", resp.Feat)
	}
	if resp.Command != resGetFeat {
		t.Errorf("command = % x, want % x", resp.Command, resGetFeat)
	}
	if !bytes.Equal(resp.Value, []byte{0x0F, 0xA0}) {
		t.Errorf("value = % x, want 0f a0", resp.Value)
	}
}

// TestParseResponseBadCRC verifies CRC mismatch is rejected.
func TestParseResponseBadCRC(t *testing.T) {
	inner := []byte{0x11, 0x22, 0x01, 0x03, 0x08, 0x09, 0x70, 0x09,
		0x03, 0x02, 0x02, 0x00, 0x01, 0x02, 0x0F, 0xA0}
	buf := make([]byte, PacketSize)
	buf[0] = 0x01
	buf[1] = byte(len(inner) + 2)
	copy(buf[2:], inner)
	buf[2+len(inner)] = 0x00 // wrong CRC
	buf[2+len(inner)+1] = 0x00

	if _, err := ParseResponse(buf); err == nil {
		t.Fatal("ParseResponse accepted bad CRC")
	}
}

// TestIsConfirmAck verifies CONFIRM ACK detection.
func TestIsConfirmAck(t *testing.T) {
	buf := make([]byte, PacketSize)
	buf[10], buf[11], buf[12] = 0x09, 0x00, 0x00
	if !IsConfirmAck(buf) {
		t.Fatal("IsConfirmAck missed [09 00 00]")
	}
	buf[10] = 0x03
	if IsConfirmAck(buf) {
		t.Fatal("IsConfirmAck false positive")
	}
}
