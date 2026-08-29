package hidraw

import (
	"bytes"
	"os"
	"testing"
)

func TestWriteReopensDisconnectedDevice(t *testing.T) {
	stale, err := os.CreateTemp(t.TempDir(), "stale")
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	replacement, err := os.CreateTemp(t.TempDir(), "replacement")
	if err != nil {
		t.Fatal(err)
	}
	d := &Device{
		f: stale,
		open: func() (*os.File, error) {
			return replacement, nil
		},
	}
	pkt := bytes.Repeat([]byte{0x5a}, 64)
	if err := d.Write(pkt); err != nil {
		t.Fatalf("Write after reconnect: %v", err)
	}
	if err := replacement.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(replacement.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pkt) {
		t.Fatalf("replacement received %d bytes, want packet", len(got))
	}
}
