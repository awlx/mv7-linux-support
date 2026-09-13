package meter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

const (
	stderrLimit = 4096
	jsonLimit   = 2 * 1024 * 1024
)

// cappedBuffer continues draining a child after the diagnostic limit is reached.
type cappedBuffer struct {
	mu        sync.Mutex
	data      bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := min(len(data), b.limit-b.data.Len())
	b.data.Write(data[:keep])
	if keep != len(data) {
		b.truncated = true
	}
	return len(data), nil
}

func (b *cappedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String(), b.truncated
}

func toolError(tool string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is unavailable; install pulseaudio-utils: %w", tool, err)
	}
	return fmt.Errorf("%s failed (requires pulseaudio-utils and a running PulseAudio/PipeWire audio service): %w", tool, err)
}

func diagnostic(err error, stderr *cappedBuffer) error {
	text, truncated := stderr.snapshot()
	text = strings.TrimSpace(text)
	if truncated {
		text += " [stderr truncated]"
	}
	if text != "" {
		return fmt.Errorf("%w; stderr: %s", err, text)
	}
	return err
}

func (b backend) list(ctx context.Context, kind string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, b.commandTimeout)
	defer cancel()
	cmd := b.command(ctx, "pactl", "--format=json", "list", kind)
	cmd.WaitDelay = b.waitDelay
	stdout := &cappedBuffer{limit: jsonLimit}
	stderr := &cappedBuffer{limit: stderrLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, diagnostic(toolError("pactl "+kind, err), stderr)
	}
	data, truncated := stdout.snapshot()
	if truncated {
		return nil, fmt.Errorf("pactl %s JSON exceeds %d-byte limit", kind, jsonLimit)
	}
	return []byte(data), nil
}
