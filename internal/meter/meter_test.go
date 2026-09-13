package meter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMeterHelperProcess substitutes this test binary for every audio command.
// It never invokes audio tools or opens an audio device.
func TestMeterHelperProcess(t *testing.T) {
	if os.Getenv("MV7_METER_TEST_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MV7_METER_TEST_MODE")
	switch mode {
	case "json":
		fmt.Fprint(os.Stdout, os.Getenv("MV7_METER_TEST_OUTPUT"))
	case "fail":
		fmt.Fprint(os.Stderr, "fixture tool failure")
		os.Exit(2)
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", stderrLimit*4))
		os.Exit(2)
	case "stall":
		time.Sleep(time.Minute)
	case "truncated":
		os.Stdout.Write([]byte{0, 0, 0})
	case "nonfinite":
		os.Stdout.Write(pcmBytes(1, func(int, int) float32 { return float32(math.NaN()) }))
	case "stream", "one-then-stall":
		channels, _ := strconv.Atoi(os.Getenv("MV7_METER_TEST_CHANNELS"))
		data := pcmBytes(channels, func(_, channel int) float32 {
			if channel%2 == 0 {
				return .5
			}
			return -.5
		})
		for {
			if _, err := os.Stdout.Write(data); err != nil {
				os.Exit(0)
			}
			if mode == "one-then-stall" {
				time.Sleep(time.Minute)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		fmt.Fprint(os.Stderr, "invalid test helper mode")
		os.Exit(3)
	}
	os.Exit(0)
}

type commandCall struct {
	name string
	args []string
	cmd  *exec.Cmd
}

type processFixture struct {
	t           *testing.T
	mu          sync.Mutex
	calls       []commandCall
	capture     *exec.Cmd
	token       string
	channels    int
	audioMode   string
	missingTool string
	counts      map[string]int
	listResult  func(kind string, count int, normal string) (mode, output string)
}

func newProcessFixture(t *testing.T) *processFixture {
	t.Helper()
	return &processFixture{t: t, channels: 1, audioMode: "stream", counts: make(map[string]int)}
}

func (f *processFixture) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMeterHelperProcess$")
	mode, output := f.audioMode, ""
	if name == f.missingTool {
		cmd.Err = &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	switch name {
	case "pactl":
		if len(args) != 3 || args[0] != "--format=json" || args[1] != "list" {
			f.t.Errorf("unexpected pactl arguments: %v", args)
		}
		kind := args[len(args)-1]
		f.counts[kind]++
		mode = "json"
		if kind == "sources" {
			s := sourceFixture()
			s["sample_specification"] = fmt.Sprintf("float32le %dch 48000Hz", f.channels)
			delete(s, "channel_map")
			output = string(jsonBytes(f.t, []any{s}))
		} else if kind == "source-outputs" && f.capture != nil && f.capture.Process != nil {
			output = string(jsonBytes(f.t, []any{outputFixture(f.capture.Process.Pid, f.token, 42)}))
		} else {
			output = "[]"
		}
		if f.listResult != nil {
			mode, output = f.listResult(kind, f.counts[kind], output)
		}
	case "parec":
		f.capture = cmd
		for _, arg := range args {
			if strings.HasPrefix(arg, "--property=mv7.meter.id=") {
				f.token = strings.TrimPrefix(arg, "--property=mv7.meter.id=")
			}
		}
	default:
		f.t.Errorf("unexpected command %q", name)
	}
	cmd.Env = append(os.Environ(),
		"MV7_METER_TEST_HELPER=1",
		"MV7_METER_TEST_MODE="+mode,
		"MV7_METER_TEST_OUTPUT="+output,
		"MV7_METER_TEST_CHANNELS="+strconv.Itoa(f.channels),
		// Race-instrumented helpers should exit without the race runtime's
		// one-second exit delay, which would obscure the meter's deadlines.
		"GORACE=atexit_sleep_ms=0",
	)
	f.calls = append(f.calls, commandCall{name: name, args: append([]string(nil), args...), cmd: cmd})
	return cmd
}

func (f *processFixture) backend() backend {
	b := defaultBackend()
	b.command = f.command
	b.commandTimeout = 400 * time.Millisecond
	b.waitDelay = 100 * time.Millisecond
	b.retryDelay = 80 * time.Millisecond
	b.audioTimeout = 500 * time.Millisecond
	b.verifyInterval = 25 * time.Millisecond
	b.publishInterval = 20 * time.Millisecond
	return b
}

func (f *processFixture) assertReaped() {
	f.t.Helper()
	for _, call := range f.calls {
		if call.cmd.Process != nil && call.cmd.ProcessState == nil {
			f.t.Errorf("child was not reaped: %s %v", call.name, call.args)
		}
	}
}

func TestRunCaptureAndCancellation(t *testing.T) {
	f := newProcessFixture(t)
	f.channels = 2
	b := f.backend()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	readings := 0
	var previous time.Time
	b.run(ctx, func(r Reading) {
		if !r.Available {
			t.Errorf("unexpected unavailable reading: %+v", r)
			cancel()
			return
		}
		if r.Source == "" || r.Error != "" || r.PeakDBFS == nil || r.RMSDBFS == nil ||
			math.Abs(*r.PeakDBFS+6.020599913) > 1e-5 || math.Abs(*r.RMSDBFS+6.020599913) > 1e-5 {
			t.Errorf("invalid live stereo reading: %+v", r)
		}
		now := time.Now()
		if !previous.IsZero() && now.Sub(previous) < b.publishInterval/2 {
			t.Error("readings published faster than the configured cadence")
		}
		previous = now
		readings++
		if readings == 3 {
			cancel()
		}
	})
	if readings != 3 {
		t.Fatalf("got %d readings before cancellation", readings)
	}
	f.assertReaped()
	for _, call := range f.calls {
		if call.name == "parec" {
			want := captureArgs(source{Name: sourceFixture()["name"].(string), Channels: 2}, f.token)
			if !reflect.DeepEqual(call.args, want) {
				t.Fatalf("incorrect capture arguments: %v", call.args)
			}
		}
	}
}

func TestCaptureArgumentsAreExact(t *testing.T) {
	name := "source with spaces;$(not-a-shell)"
	args := captureArgs(source{Name: name, Channels: 8}, "unique-token")
	expected := []string{
		"--raw", "--format=float32le", "--rate=48000", "--channels=8",
		"--device=" + name, "--latency-msec=100",
		"--client-name=MV7+ level meter", "--stream-name=MV7+ live input level",
		"--property=node.dont-reconnect=true", "--property=node.dont-fallback=true",
		"--property=node.dont-move=true",
		"--property=mv7.meter.id=unique-token",
	}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("got %v, want %v", args, expected)
	}
}

func TestRunMissingTools(t *testing.T) {
	for _, tool := range []string{"pactl", "parec"} {
		t.Run(tool, func(t *testing.T) {
			f := newProcessFixture(t)
			f.missingTool = tool
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var reading Reading
			f.backend().run(ctx, func(r Reading) { reading = r; cancel() })
			if reading.Available || !strings.Contains(reading.Error, "install pulseaudio-utils") ||
				!strings.Contains(reading.Error, tool) || reading.PeakDBFS != nil || reading.RMSDBFS != nil {
				t.Fatalf("missing-tool diagnostic: %+v", reading)
			}
			f.assertReaped()
		})
	}
}

func TestRunRetriesDiscoveryWithoutDefault(t *testing.T) {
	f := newProcessFixture(t)
	f.listResult = func(kind string, count int, normal string) (string, string) {
		if kind == "sources" && count == 1 {
			return "json", "[]"
		}
		return "json", normal
	}
	b := f.backend()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var unavailableAt time.Time
	gotLevel := false
	b.run(ctx, func(r Reading) {
		if !r.Available {
			if !strings.Contains(r.Error, "refusing default input") {
				t.Errorf("unexpected discovery error: %s", r.Error)
			}
			unavailableAt = time.Now()
			if f.capture != nil {
				t.Error("capture started before a verified USB source existed")
			}
			return
		}
		if unavailableAt.IsZero() || time.Since(unavailableAt) < b.retryDelay {
			t.Error("retry did not wait for its backoff")
		}
		gotLevel = true
		cancel()
	})
	if !gotLevel {
		t.Fatal("capture did not recover after discovery retry")
	}
	f.assertReaped()
}

func TestRunRetriesCaptureWithFreshIdentity(t *testing.T) {
	f := newProcessFixture(t)
	f.audioMode = "fail"
	b := f.backend()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var failedAt time.Time
	firstToken := ""
	levels := 0
	b.run(ctx, func(r Reading) {
		if !r.Available {
			if !failedAt.IsZero() || !strings.Contains(r.Error, "fixture tool failure") {
				t.Errorf("unexpected capture failure: %s", r.Error)
				cancel()
				return
			}
			f.assertReaped()
			firstToken = f.token
			failedAt = time.Now()
			f.audioMode = "stream"
			return
		}
		if failedAt.IsZero() || time.Since(failedAt) < b.retryDelay {
			t.Error("capture retry did not wait for its backoff")
		}
		if f.token == "" || f.token == firstToken {
			t.Error("capture retry reused a stream identity")
		}
		levels++
		cancel()
	})
	if levels != 1 {
		t.Fatalf("expected recovery, got %d readings", levels)
	}
	f.assertReaped()
}

func TestDelayedStreamRegistration(t *testing.T) {
	f := newProcessFixture(t)
	f.listResult = func(kind string, count int, normal string) (string, string) {
		if kind == "source-outputs" && count < 3 {
			return "json", "[]"
		}
		return "json", normal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	levels := 0
	f.backend().run(ctx, func(r Reading) {
		if !r.Available {
			t.Errorf("failed before stream registration: %s", r.Error)
		} else {
			levels++
			f.mu.Lock()
			registered := f.counts["source-outputs"] >= 3
			f.mu.Unlock()
			if !registered {
				t.Error("published before stream was verifiable")
			}
		}
		cancel()
	})
	if levels != 1 {
		t.Fatalf("expected one registered-stream reading, got %d", levels)
	}
	f.assertReaped()
}

func TestOldUnverifiedChunkIsNotPublished(t *testing.T) {
	f := newProcessFixture(t)
	f.audioMode = "one-then-stall"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var firstOutput time.Time
	f.listResult = func(kind string, _ int, normal string) (string, string) {
		if kind == "source-outputs" {
			if firstOutput.IsZero() {
				firstOutput = time.Now()
			}
			if time.Since(firstOutput) < maxReadingAge+100*time.Millisecond {
				return "json", "[]"
			}
		}
		return "json", normal
	}
	failures := 0
	f.backend().run(ctx, func(r Reading) {
		if r.Available {
			t.Error("published an old chunk when stream registration became visible")
		} else {
			failures++
			if !strings.Contains(r.Error, "watchdog deadline") {
				t.Errorf("expected watchdog failure, got %s", r.Error)
			}
		}
		cancel()
	})
	if failures != 1 {
		t.Fatalf("expected unavailable reading, got %d", failures)
	}
	f.assertReaped()
}

func TestRunFailuresAndWatchdogs(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
		config   func(*processFixture)
	}{
		{"initial audio timeout", "stall", "watchdog deadline", nil},
		{"nonfinite", "nonfinite", "non-finite", nil},
		{"truncated float", "truncated", "truncated terminal PCM float", nil},
		{"parec failure", "fail", "fixture tool failure", nil},
		{"bounded stderr", "stderr", "stderr truncated", nil},
		{"pactl failure", "stream", "fixture tool failure", func(f *processFixture) {
			f.listResult = func(string, int, string) (string, string) { return "fail", "" }
		}},
		{"pactl timeout", "stream", "deadline exceeded", func(f *processFixture) {
			f.listResult = func(string, int, string) (string, string) { return "stall", "" }
		}},
		{"unverified stream startup", "stream", "startup timed out", func(f *processFixture) {
			f.listResult = func(kind string, _ int, normal string) (string, string) {
				if kind == "source-outputs" {
					return "json", "[]"
				}
				return "json", normal
			}
		}},
		{"misrouted initial stream", "stream", "moved to source", func(f *processFixture) {
			f.listResult = func(kind string, _ int, normal string) (string, string) {
				if kind == "source-outputs" {
					return "json", strings.Replace(normal, `"source":42`, `"source":7`, 1)
				}
				return "json", normal
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newProcessFixture(t)
			f.audioMode = test.mode
			if test.config != nil {
				test.config(f)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var got Reading
			f.backend().run(ctx, func(r Reading) {
				if r.Available {
					t.Error("published an unverified or invalid input")
					return
				}
				got = r
				cancel()
			})
			if got.Available || !strings.Contains(got.Error, test.expected) {
				t.Fatalf("got %+v, expected %q", got, test.expected)
			}
			if got.PeakDBFS != nil || got.RMSDBFS != nil || got.Clipping {
				t.Fatalf("failure retained old level values: %+v", got)
			}
			if test.mode == "stderr" && len(got.Error) > stderrLimit+500 {
				t.Fatalf("unbounded stderr: %d bytes", len(got.Error))
			}
			f.assertReaped()
		})
	}
}

func TestStalledAudioDoesNotRepeatStaleReadings(t *testing.T) {
	f := newProcessFixture(t)
	f.audioMode = "one-then-stall"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	levels, failures := 0, 0
	f.backend().run(ctx, func(r Reading) {
		if r.Available {
			levels++
		} else {
			failures++
			if !strings.Contains(r.Error, "stalled") {
				t.Errorf("expected stalled error, got %s", r.Error)
			}
			cancel()
		}
	})
	if levels != 1 || failures != 1 {
		t.Fatalf("got %d live and %d unavailable readings, want 1 each", levels, failures)
	}
	f.assertReaped()
}

func TestRouteSupervision(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     string
		change   func(string) string
		expected string
	}{
		{"removed source", "sources", func(string) string { return "[]" }, "source disconnected"},
		{"replaced source", "sources", func(s string) string { return strings.Replace(s, `"index":42`, `"index":43`, 1) }, "identity or channel layout changed"},
		{"removed stream", "source-outputs", func(string) string { return "[]" }, "not present"},
		{"moved stream", "source-outputs", func(s string) string { return strings.Replace(s, `"source":42`, `"source":7`, 1) }, "moved to source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newProcessFixture(t)
			f.listResult = func(kind string, count int, normal string) (string, string) {
				if kind == test.kind && count >= 5 {
					return "json", test.change(normal)
				}
				return "json", normal
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			levels := 0
			failed := false
			f.backend().run(ctx, func(r Reading) {
				if r.Available {
					levels++
				} else {
					failed = true
					if !strings.Contains(r.Error, test.expected) {
						t.Errorf("expected %q, got %s", test.expected, r.Error)
					}
					cancel()
				}
			})
			if levels == 0 || !failed {
				t.Fatalf("supervision did not invalidate an active stream: levels=%d failed=%v", levels, failed)
			}
			f.assertReaped()
		})
	}
}

func TestCancellationDuringDiscoveryAndCapture(t *testing.T) {
	for _, phase := range []string{"discovery", "capture", "retry"} {
		t.Run(phase, func(t *testing.T) {
			f := newProcessFixture(t)
			b := f.backend()
			if phase == "discovery" {
				f.listResult = func(string, int, string) (string, string) { return "stall", "" }
			} else if phase == "capture" {
				f.audioMode = "stall"
			} else {
				f.missingTool = "pactl"
				b.retryDelay = time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer cancel()
			start := time.Now()
			emissions := 0
			b.run(ctx, func(r Reading) { emissions++ })
			if time.Since(start) > time.Second {
				t.Fatal("cancellation was not prompt")
			}
			if phase != "retry" && emissions != 0 {
				t.Errorf("emitted %d readings after cancellation", emissions)
			}
			f.assertReaped()
		})
	}
}

func TestDefaultConfigurationAndCanceledRun(t *testing.T) {
	b := defaultBackend()
	if b.retryDelay != 2*time.Second || b.audioTimeout != 3*time.Second ||
		b.publishInterval != 100*time.Millisecond || b.verifyInterval != 500*time.Millisecond {
		t.Fatalf("unexpected production timing: %+v", b)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, func(Reading) { t.Fatal("already-canceled Run emitted a reading") })
}

func TestReadingJSON(t *testing.T) {
	zero := 0.0
	for _, r := range []Reading{
		{Available: true, PeakDBFS: &zero, RMSDBFS: &zero, Clipping: true, Source: "MV7+"},
		{Error: "disconnected"},
	} {
		data := jsonBytes(t, r)
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		_, hasPeak := fields["peak_dbfs"]
		_, hasRMS := fields["rms_dbfs"]
		if hasPeak != r.Available || hasRMS != r.Available {
			t.Fatalf("incorrect level field presence: %s", data)
		}
		if _, present := fields["clipping"]; !present {
			t.Fatalf("clipping omitted: %s", data)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{limit: stderrLimit}
	payload := strings.Repeat("a", stderrLimit*3)
	if n, err := b.Write([]byte(payload)); err != nil || n != len(payload) {
		t.Fatalf("buffer did not drain input: %d %v", n, err)
	}
	text, truncated := b.snapshot()
	if len(text) != stderrLimit || !truncated {
		t.Fatalf("incorrect cap: %d %v", len(text), truncated)
	}
	err := diagnostic(errors.New("capture failed"), b)
	if !strings.Contains(err.Error(), "[stderr truncated]") {
		t.Fatalf("missing truncation marker: %v", err)
	}
}
