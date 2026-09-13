package server

import (
	"context"
	"time"

	"github.com/awlx/mv7-linux-support/internal/meter"
)

func (s *Server) wakeMeter() {
	select {
	case s.meterWake <- struct{}{}:
	default:
	}
}

func (s *Server) setMeterSubscription(c *client, enabled bool) {
	s.mu.Lock()
	if s.clients[c] {
		c.wantsMeter = enabled
		message := "Live input meter disabled"
		if enabled {
			message = "Waiting for MV7+ audio input"
		}
		if !c.submit(wsEvent{Type: "meter", Meter: &meter.Reading{Error: message}}) {
			delete(s.clients, c)
			c.close()
		}
	}
	s.mu.Unlock()
	s.wakeMeter()
}

func (s *Server) meterWanted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateMu.RLock()
	connected := s.connected
	s.stateMu.RUnlock()
	if connected {
		for c := range s.clients {
			if c.wantsMeter {
				return true
			}
		}
	}
	return false
}

func (s *Server) publishMeter(ctx context.Context, reading meter.Reading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	s.stateMu.RLock()
	connected := s.connected
	s.stateMu.RUnlock()
	if !connected {
		reading = meter.Reading{Error: "Microphone unavailable"}
	}
	for c := range s.clients {
		if c.wantsMeter && !c.submit(wsEvent{Type: "meter", Meter: &reading}) {
			delete(s.clients, c)
			c.close()
			s.wakeMeter()
		}
	}
}

// The audio stream is shared by subscribers and never runs in the HID worker.
func (s *Server) runMeter(stop <-chan struct{}) {
	var cancel context.CancelFunc
	var finished chan struct{}
	var retry <-chan time.Time
	stopCapture := func() {
		if cancel != nil {
			cancel()
			<-finished
			cancel = nil
			finished = nil
		}
	}
	defer stopCapture()
	for {
		wanted := s.meterWanted()
		if !wanted {
			stopCapture()
		} else if cancel == nil && retry == nil {
			ctx, stopRun := context.WithCancel(context.Background())
			cancel = stopRun
			finished = make(chan struct{})
			go func(done chan struct{}) {
				defer close(done)
				s.meterRun(ctx, func(reading meter.Reading) { s.publishMeter(ctx, reading) })
			}(finished)
		}
		select {
		case <-stop:
			return
		case <-s.meterWake:
		case <-retry:
			retry = nil
		case <-finished:
			cancel()
			cancel = nil
			finished = nil
			s.publishMeter(context.Background(), meter.Reading{Error: "Audio meter stopped; retrying"})
			retry = time.After(2 * time.Second)
		}
	}
}
