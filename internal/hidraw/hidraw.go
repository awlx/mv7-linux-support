// Package hidraw provides access to the Shure MV7+ vendor HID console via
// the Linux hidraw character devices (/dev/hidrawN).
package hidraw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Shure USB IDs.
const (
	VendorID  = 0x14ED
	ProductID = 0x1019 // MV7+
)

// Device is an open hidraw device.
type Device struct {
	mu         sync.Mutex
	f          *os.File
	open       func() (*os.File, error)
	generation uint64
	closed     bool
}

// Find locates the MV7+ vendor console hidraw device by scanning /sys for the
// HID interface whose uevent HID_ID matches 0003:000014ED:00001019, then opens
// the corresponding /dev/hidrawN node.
func Find() (*Device, error) {
	f, err := findFile()
	if err != nil {
		return nil, err
	}
	return &Device{f: f, open: findFile}, nil
}

func findFile() (*os.File, error) {
	hidraws, err := filepath.Glob("/sys/class/hidraw/hidraw*")
	if err != nil || len(hidraws) == 0 {
		return nil, errors.New("no hidraw devices found (is the MV7+ plugged in?)")
	}

	for _, sysPath := range hidraws {
		name := filepath.Base(sysPath)
		uevent, err := os.ReadFile(filepath.Join(sysPath, "device", "uevent"))
		if err != nil {
			continue
		}
		if !strings.Contains(string(uevent), "HID_ID=0003:000014ED:00001019") {
			continue
		}

		// Verify the report descriptor starts with usage page 0xFF09 (vendor).
		rdesc, err := os.ReadFile(filepath.Join(sysPath, "device", "report_descriptor"))
		if err == nil && len(rdesc) >= 4 {
			if rdesc[0] != 0x06 || rdesc[1] != 0x01 || rdesc[2] != 0xFF || rdesc[3] != 0x09 {
				continue
			}
		}

		devPath := "/dev/" + name
		// Open non-blocking so ReadTimeout can poll with a deadline.
		f, err := os.OpenFile(devPath, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("cannot open %s: %w (is the udev rule installed? run as root?)", devPath, err)
		}
		return f, nil
	}

	return nil, errors.New("no Shure MV7+ (14ed:1019) vendor console found")
}

// Write writes a full 64-byte report to the device.
func (d *Device) Write(pkt []byte) error {
	if len(pkt) != 64 {
		return fmt.Errorf("hidraw write requires 64 bytes, got %d", len(pkt))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("hidraw device is closed")
	}
	if err := d.write(pkt); err == nil {
		return nil
	}
	if err := d.reopen(); err != nil {
		return err
	}
	return d.write(pkt)
}

func (d *Device) write(pkt []byte) error {
	n, err := d.f.Write(pkt)
	if err != nil {
		return err
	}
	if n != 64 {
		return fmt.Errorf("hidraw short write: %d/64 bytes", n)
	}
	return nil
}

func (d *Device) reopen() error {
	open := d.open
	if open == nil {
		open = findFile
	}
	f, err := open()
	if err != nil {
		return fmt.Errorf("MV7+ disconnected: %w", err)
	}
	_ = d.f.Close()
	d.f = f
	d.generation++
	return nil
}

// Generation changes whenever the underlying hidraw descriptor is reopened.
func (d *Device) Generation() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.generation
}

// ReadTimeout reads up to len(buf) bytes with a timeout. Returns the number of
// bytes read, or an error on timeout.
func (d *Device) ReadTimeout(buf []byte, timeout time.Duration) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, errors.New("hidraw device is closed")
	}
	deadline := time.Now().Add(timeout)
	for {
		n, err := syscall.Read(int(d.f.Fd()), buf)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			return 0, err
		}
		if time.Now().After(deadline) {
			return 0, errors.New("read timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// Close closes the device.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.f.Close()
}
