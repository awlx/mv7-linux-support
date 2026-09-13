package meter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Reading contains only scalar input levels, never audio samples. Unavailable
// readings have an explicit Error and no level values.
type Reading struct {
	Available bool     `json:"available"`
	PeakDBFS  *float64 `json:"peak_dbfs,omitempty"`
	RMSDBFS   *float64 `json:"rms_dbfs,omitempty"`
	Clipping  bool     `json:"clipping"`
	Source    string   `json:"source,omitempty"`
	Error     string   `json:"error,omitempty"`
}

const (
	captureClientName = "MV7+ level meter"
	captureStreamName = "MV7+ live input level"
	maxReadingAge     = 250 * time.Millisecond
)

type backend struct {
	command         func(context.Context, string, ...string) *exec.Cmd
	commandTimeout  time.Duration
	waitDelay       time.Duration
	retryDelay      time.Duration
	audioTimeout    time.Duration
	verifyInterval  time.Duration
	publishInterval time.Duration
}

func defaultBackend() backend {
	return backend{
		command:         exec.CommandContext,
		commandTimeout:  time.Second,
		waitDelay:       200 * time.Millisecond,
		retryDelay:      2 * time.Second,
		audioTimeout:    3 * time.Second,
		verifyInterval:  500 * time.Millisecond,
		publishInterval: 100 * time.Millisecond,
	}
}

// Run captures a uniquely identified MV7+ input until ctx is canceled, retrying
// unavailable sources and capture failures every two seconds. It emits fresh
// readings at most ten times a second, with a three-second startup/stall bound.
// Chunks completed more than 250 ms ago are not published.
// emit is called synchronously and must be non-nil and return promptly.
// Cancellation stops and reaps all children before Run returns; it does not
// emit a final reading. Callers should clear their cached level on cancellation.
func Run(ctx context.Context, emit func(Reading)) {
	defaultBackend().run(ctx, emit)
}

func (b backend) run(ctx context.Context, emit func(Reading)) {
	for ctx.Err() == nil {
		data, err := b.list(ctx, "sources")
		var selected source
		if err == nil {
			selected, err = selectSource(data)
		}
		if err == nil {
			err = b.capture(ctx, selected, emit)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			emit(Reading{Source: selected.Name, Error: err.Error()})
		}
		if !pause(ctx, b.retryDelay) {
			return
		}
	}
}

func captureArgs(selected source, token string) []string {
	return []string{
		"--raw",
		"--format=float32le",
		"--rate=" + strconv.Itoa(sampleRate),
		"--channels=" + strconv.Itoa(selected.Channels),
		"--device=" + selected.Name,
		"--latency-msec=100",
		"--client-name=" + captureClientName,
		"--stream-name=" + captureStreamName,
		"--property=node.dont-reconnect=true",
		"--property=node.dont-fallback=true",
		"--property=node.dont-move=true",
		"--property=mv7.meter.id=" + token,
	}
}

func (b backend) capture(ctx context.Context, selected source, emit func(Reading)) (resultErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("cannot create capture stream identity: %w", err)
	}
	token := hex.EncodeToString(random[:])
	cmd := b.command(ctx, "parec", captureArgs(selected, token)...)
	cmd.WaitDelay = b.waitDelay
	stderr := &cappedBuffer{limit: stderrLimit}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return toolError("parec stdout", err)
	}
	if err := cmd.Start(); err != nil {
		stdout.Close()
		return toolError("parec", err)
	}

	pcm := make(chan pcmResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readPCM(ctx, stdout, selected.Channels, pcm)
	}()
	routes := make(chan error, 1)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		b.monitor(ctx, selected, cmd.Process.Pid, token, routes)
	}()
	defer func() {
		cancel()
		stdout.Close()
		<-readerDone
		<-monitorDone
		waitErr := cmd.Wait()
		if resultErr == nil && waitErr != nil {
			resultErr = toolError("parec", waitErr)
		}
		if resultErr != nil {
			resultErr = diagnostic(resultErr, stderr)
		}
	}()

	initial := time.NewTimer(b.audioTimeout)
	defer initial.Stop()
	startup := initial.C
	watchdog := time.NewTimer(b.audioTimeout)
	defer watchdog.Stop()
	publish := time.NewTimer(b.publishInterval)
	defer publish.Stop()
	var pending *pcmResult
	verified := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-startup:
			return errors.New("MV7+ capture startup timed out: no verified, complete audio chunk within watchdog deadline")
		case <-watchdog.C:
			return errors.New("MV7+ audio stalled: no complete PCM chunk within watchdog deadline")
		case err := <-routes:
			if err != nil {
				return err
			}
			verified = true
		case result := <-pcm:
			if result.err != nil {
				return fmt.Errorf("MV7+ capture: %w", result.err)
			}
			watchdog.Reset(b.audioTimeout)
			result.reading.Source = selected.Name
			pending = &result
		case <-publish.C:
			if pending != nil && time.Since(pending.completed) > maxReadingAge {
				pending = nil
			}
			if verified && pending != nil && ctx.Err() == nil {
				emit(pending.reading)
				pending = nil
				if startup != nil {
					initial.Stop()
					startup = nil
				}
			}
			publish.Reset(b.publishInterval)
		}
	}
}

func (b backend) monitor(ctx context.Context, selected source, pid int, token string, results chan<- error) {
	verified := false
	for ctx.Err() == nil {
		err := b.verifyRoute(ctx, selected, pid, token)
		if !verified && errors.Is(err, errStreamPending) {
			if !pause(ctx, min(b.verifyInterval, 100*time.Millisecond)) {
				return
			}
			continue
		}
		select {
		case results <- err:
		case <-ctx.Done():
			return
		}
		if err != nil || !pause(ctx, b.verifyInterval) {
			return
		}
		verified = true
	}
}

func (b backend) verifyRoute(ctx context.Context, selected source, pid int, token string) error {
	data, err := b.list(ctx, "sources")
	if err != nil {
		return fmt.Errorf("cannot verify MV7+ source: %w", err)
	}
	current, err := selectSource(data)
	if err != nil {
		return fmt.Errorf("MV7+ source disconnected or changed: %w", err)
	}
	if current != selected {
		return errors.New("MV7+ source identity or channel layout changed; stopping capture")
	}
	data, err = b.list(ctx, "source-outputs")
	if err != nil {
		return fmt.Errorf("cannot verify MV7+ capture routing: %w", err)
	}
	return verifyOutput(data, selected, pid, token)
}

func pause(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
