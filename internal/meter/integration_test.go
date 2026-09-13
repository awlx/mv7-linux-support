package meter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIntegrationDontMoveSurvivesEasyEffectsMetadataMove is opt-in
// (MV7_METER_INTEGRATION=1) and requires a live PulseAudio/PipeWire server
// with a synthetic MV7+ source named mv7_test (USB 14ed:1019) and a second
// non-USB source named effects_test, both marked mv7.test.synthetic=true.
// It verifies that setting target.node and
// target.object metadata on the capture stream (the EasyEffects reroute
// mechanism) does not move the stream away from the MV7+ source, because
// node.dont-move=true is requested on the stream.
func TestIntegrationDontMoveSurvivesEasyEffectsMetadataMove(t *testing.T) {
	if os.Getenv("MV7_METER_INTEGRATION") != "1" {
		t.Skip("set MV7_METER_INTEGRATION=1 to run the live PipeWire integration test")
	}
	for _, tool := range []string{"pactl", "parec", "pw-dump", "pw-metadata"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("integration test requires %s: %v", tool, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := defaultBackend()

	data, err := b.list(ctx, "sources")
	if err != nil {
		t.Fatalf("cannot list sources: %v", err)
	}
	selected, err := selectSource(data)
	if err != nil {
		t.Fatalf("no synthetic MV7+ source (USB 14ed:1019): %v", err)
	}
	if selected.Name != "mv7_test" {
		t.Fatalf("refusing to capture non-fixture source %q", selected.Name)
	}
	var sources []pulseSource
	if err := json.Unmarshal(data, &sources); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mv7_test", "effects_test"} {
		marked := false
		for _, candidate := range sources {
			var marker string
			if candidate.Name == name && json.Unmarshal(candidate.Properties["mv7.test.synthetic"], &marker) == nil {
				marked = marker == "true"
			}
		}
		if !marked {
			t.Fatalf("refusing integration test without explicitly marked synthetic source %q", name)
		}
	}

	identity := make(chan string, 1)
	command := b.command
	b.command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "parec" {
			for _, arg := range args {
				if token, ok := strings.CutPrefix(arg, "--property=mv7.meter.id="); ok {
					identity <- token
				}
			}
		}
		return command(ctx, name, args...)
	}

	// Start the production capture and wait for the first verified reading.
	readings := make(chan Reading, 16)
	captureCtx, stopCapture := context.WithCancel(ctx)
	captureDone := make(chan error, 1)
	t.Cleanup(func() {
		stopCapture()
		select {
		case err := <-captureDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("capture returned unexpected error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("capture children did not stop within cleanup deadline")
		}
	})
	go func() {
		captureDone <- b.capture(captureCtx, selected, func(r Reading) {
			select {
			case readings <- r:
			default:
			}
		})
	}()
	if r := waitReading(t, readings, 5*time.Second); !r.Available {
		t.Fatalf("capture did not become available: %+v", r)
	}
	var token string
	select {
	case token = <-identity:
		if token == "" {
			t.Fatal("capture token is empty")
		}
	case <-ctx.Done():
		t.Fatal("capture token was not observed")
	}

	// Find the capture stream node and the decoy effects_test node.
	captureID := pwNodeID(t, func(props map[string]string) bool {
		return props["mv7.meter.id"] == token
	})
	if captureID == 0 {
		t.Fatal("capture stream node not found in pw-dump")
	}
	effectsID, effectsSerial := pwNodeIDAndSerial(t, func(props map[string]string) bool {
		return props["node.name"] == "effects_test"
	})
	if effectsID == 0 {
		t.Fatal("no effects_test node found; create a non-USB source named effects_test")
	}

	// Simulate EasyEffects: set target.node and target.object metadata on the
	// capture stream node, pointing at the decoy source.
	runTool(t, "pw-metadata", "-n", "default", strconv.Itoa(captureID), "target.node", strconv.Itoa(effectsID), "Spa:Id")
	runTool(t, "pw-metadata", "-n", "default", strconv.Itoa(captureID), "target.object", strconv.Itoa(effectsSerial), "Spa:Id")

	// Give WirePlumber time to act on the metadata move request.
	time.Sleep(2 * time.Second)

	// The stream must still be routed to the MV7+ source and still producing.
	if idx := captureSourceIndex(t, b, ctx, token); idx != int(selected.Index) {
		t.Fatalf("capture stream moved to source %d (expected %d); node.dont-move was not honored", idx, selected.Index)
	}
	sourceID := pwNodeID(t, func(props map[string]string) bool {
		return props["node.name"] == selected.Name
	})
	if sourceID == 0 {
		t.Fatal("synthetic MV7+ native node disappeared")
	}
	assertCaptureLinks(t, captureID, sourceID)
	drainReadings(readings)
	if r := waitReading(t, readings, 5*time.Second); !r.Available {
		t.Fatalf("capture stopped producing after metadata move: %+v", r)
	}
}

func waitReading(t *testing.T, readings <-chan Reading, timeout time.Duration) Reading {
	t.Helper()
	select {
	case r := <-readings:
		return r
	case <-time.After(timeout):
		t.Fatalf("no reading within %s", timeout)
		return Reading{}
	}
}

func drainReadings(readings <-chan Reading) {
	for {
		select {
		case <-readings:
		default:
			return
		}
	}
}

// pwNodeID finds a PipeWire node id whose properties match.
func pwNodeID(t *testing.T, match func(map[string]string) bool) int {
	t.Helper()
	id, _ := pwNodeIDAndSerial(t, match)
	return id
}

// pwNodeIDAndSerial finds a PipeWire node's id and serial whose properties match.
func pwNodeIDAndSerial(t *testing.T, match func(map[string]string) bool) (id, serial int) {
	t.Helper()
	out := runTool(t, "pw-dump")
	var nodes []struct {
		ID   int    `json:"id"`
		Type string `json:"type"`
		Info struct {
			Props map[string]json.RawMessage `json:"props"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("invalid pw-dump JSON: %v", err)
	}
	foundID, foundSerial := 0, 0
	for _, n := range nodes {
		if n.Type != "PipeWire:Interface:Node" {
			continue
		}
		props := make(map[string]string, len(n.Info.Props))
		for key, raw := range n.Info.Props {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				text = string(raw)
			}
			props[key] = text
		}
		if match(props) {
			if foundID != 0 {
				t.Fatal("ambiguous PipeWire node identity")
			}
			serial, err := strconv.Atoi(props["object.serial"])
			if err != nil || serial <= 0 || n.ID <= 0 {
				t.Fatalf("invalid PipeWire node identity: id=%d serial=%q", n.ID, props["object.serial"])
			}
			foundID, foundSerial = n.ID, serial
		}
	}
	return foundID, foundSerial
}

// captureSourceIndex returns the pactl source index of the capture stream
// identified by this capture's random token, or -1 if absent.
func captureSourceIndex(t *testing.T, b backend, ctx context.Context, token string) int {
	t.Helper()
	data, err := b.list(ctx, "source-outputs")
	if err != nil {
		t.Fatalf("cannot list source-outputs: %v", err)
	}
	var outputs []struct {
		Source     json.RawMessage            `json:"source"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &outputs); err != nil {
		t.Fatalf("invalid pactl source-outputs JSON: %v", err)
	}
	for _, o := range outputs {
		var identity string
		if json.Unmarshal(o.Properties["mv7.meter.id"], &identity) != nil || identity != token {
			continue
		}
		idx, err := sourceIndex(o.Source)
		if err != nil {
			t.Fatalf("capture stream source index: %v", err)
		}
		return int(idx)
	}
	return -1
}

func assertCaptureLinks(t *testing.T, captureID, sourceID int) {
	t.Helper()
	var objects []struct {
		Type string `json:"type"`
		Info struct {
			Input  int `json:"input-node-id"`
			Output int `json:"output-node-id"`
		} `json:"info"`
	}
	if err := json.Unmarshal(runTool(t, "pw-dump"), &objects); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, object := range objects {
		if object.Type == "PipeWire:Interface:Link" && object.Info.Input == captureID {
			if object.Info.Output != sourceID {
				t.Fatalf("unexpected native input link from %d; expected only %d", object.Info.Output, sourceID)
			}
			count++
		}
	}
	if count == 0 {
		t.Fatal("capture has no native input links")
	}
}

func runTool(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	out := &cappedBuffer{limit: jsonLimit}
	stderr := &cappedBuffer{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v failed: %v", name, args, diagnostic(err, stderr))
	}
	data, truncated := out.snapshot()
	if truncated {
		t.Fatalf("%s output exceeds %d bytes", name, jsonLimit)
	}
	return []byte(data)
}
