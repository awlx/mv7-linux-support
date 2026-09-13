package meter

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func sourceFixture() map[string]any {
	return map[string]any{
		"index":                42,
		"name":                 "alsa_input.usb-Shure_MV7+_ABC-00.mono-fallback",
		"monitor_of_sink":      "n/a",
		"sample_specification": "s32le 1ch 48000Hz",
		"channel_map":          "mono",
		"properties": map[string]any{
			"device.vendor.id":  "0x14ed",
			"device.product.id": "0x1019",
		},
	}
}

func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSelectSource(t *testing.T) {
	tests := []struct {
		name   string
		modify func(map[string]any)
		ok     bool
	}{
		{"normal string sentinel", func(map[string]any) {}, true},
		{"numeric sentinel", func(s map[string]any) { s["monitor_of_sink"] = uint32(4294967295) }, true},
		{"uppercase hex", func(s map[string]any) {
			s["properties"] = map[string]string{"device.vendor.id": "0X14ED", "device.product.id": "0X1019"}
		}, true},
		{"unprefixed hex", func(s map[string]any) {
			s["properties"] = map[string]string{"device.vendor.id": "14ED", "device.product.id": "1019"}
		}, true},
		{"string index", func(s map[string]any) { s["index"] = "42" }, true},
		{"monitor name", func(s map[string]any) { s["name"] = "alsa_output.Shure_MV7+.monitor" }, false},
		{"uppercase monitor name", func(s map[string]any) { s["name"] = "alsa_output.Shure_MV7+.MONITOR" }, false},
		{"numeric monitor", func(s map[string]any) { s["monitor_of_sink"] = 0 }, false},
		{"string monitor", func(s map[string]any) { s["monitor_of_sink"] = "4" }, false},
		{"null monitor", func(s map[string]any) { s["monitor_of_sink"] = nil }, false},
		{"missing monitor", func(s map[string]any) { delete(s, "monitor_of_sink") }, false},
		{"PulseAudio empty monitor source", func(s map[string]any) {
			delete(s, "monitor_of_sink")
			s["monitor_source"] = ""
		}, true},
		{"PulseAudio named monitor source", func(s map[string]any) {
			delete(s, "monitor_of_sink")
			s["monitor_source"] = "auto_null"
		}, false},
		{"PulseAudio null monitor source", func(s map[string]any) {
			delete(s, "monitor_of_sink")
			s["monitor_source"] = nil
		}, false},
		{"PulseAudio numeric monitor source", func(s map[string]any) {
			delete(s, "monitor_of_sink")
			s["monitor_source"] = 0
		}, false},
		{"consistent normal monitor fields", func(s map[string]any) {
			s["monitor_source"] = ""
		}, true},
		{"conflicting named monitor source", func(s map[string]any) {
			s["monitor_source"] = "auto_null"
		}, false},
		{"conflicting numeric monitor sink", func(s map[string]any) {
			s["monitor_of_sink"] = 4
			s["monitor_source"] = ""
		}, false},
		{"missing IDs", func(s map[string]any) { delete(s, "properties") }, false},
		{"wrong product", func(s map[string]any) {
			s["properties"].(map[string]any)["device.product.id"] = "0x1018"
		}, false},
		{"wrong vendor", func(s map[string]any) {
			s["properties"].(map[string]any)["device.vendor.id"] = "0x1234"
		}, false},
		{"missing product", func(s map[string]any) {
			delete(s["properties"].(map[string]any), "device.product.id")
		}, false},
		{"not hex", func(s map[string]any) {
			s["properties"].(map[string]any)["device.vendor.id"] = "Shure"
		}, false},
		{"empty name", func(s map[string]any) { s["name"] = "" }, false},
		{"missing index", func(s map[string]any) { delete(s, "index") }, false},
		{"invalid index", func(s map[string]any) { s["index"] = -1 }, false},
		{"null index", func(s map[string]any) { s["index"] = nil }, false},
		{"sentinel index", func(s map[string]any) { s["index"] = uint32(4294967295) }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := sourceFixture()
			test.modify(input)
			got, err := selectSource(jsonBytes(t, []any{input}))
			if test.ok {
				if err != nil || got.Index != 42 || got.Channels != 1 {
					t.Fatalf("unexpected selection %+v: %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("unsafe source accepted: %+v", got)
			}
		})
	}
}

func TestSelectSourcePulseAudio16JSON(t *testing.T) {
	data := []byte(`[{"index":1,"state":"SUSPENDED","name":"mv7_test","description":"Synthetic FIFO source","driver":"module-pipe-source.c","sample_specification":"float32le 1ch 48000Hz","channel_map":"mono","owner_module":20,"mute":false,"monitor_source":"","properties":{"device.string":"fixture.fifo","device.vendor.id":"0x14ed","device.product.id":"0x1019","device.icon_name":"audio-input-microphone"},"formats":["pcm"]}]`)
	selected, err := selectSource(data)
	if err != nil || selected != (source{Index: 1, Name: "mv7_test", Channels: 1}) {
		t.Fatalf("PulseAudio 16 source not recognized: %+v, %v", selected, err)
	}
	monitor := strings.Replace(string(data), `"monitor_source":""`, `"monitor_source":"auto_null"`, 1)
	if _, err := selectSource([]byte(monitor)); err == nil {
		t.Fatal("PulseAudio 16 monitor source was accepted")
	}
}

func TestSelectSourceUniqueAndExact(t *testing.T) {
	wrong := sourceFixture()
	wrong["name"] = "default-microphone"
	wrong["properties"] = map[string]string{}
	monitor := sourceFixture()
	monitor["monitor_of_sink"] = 3
	correct := sourceFixture()
	correct["name"] = "exact source name with spaces"
	selected, err := selectSource(jsonBytes(t, []any{wrong, monitor, correct}))
	if err != nil || selected.Name != correct["name"] {
		t.Fatalf("did not choose exact USB input: %+v %v", selected, err)
	}
	if _, err := selectSource(jsonBytes(t, []any{correct, sourceFixture()})); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error, got %v", err)
	}
	for _, data := range []string{"[]", "null", "{bad JSON}", "{}"} {
		if _, err := selectSource([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestNativeChannels(t *testing.T) {
	tests := []struct {
		spec any
		cmap any
		want int
	}{
		{"s32le 2ch 48000Hz", "front-left,front-right", 2},
		{nil, "front-left,front-right", 2},
		{"s16le 1ch 44100Hz", nil, 1},
		{map[string]any{"channels": 2}, []string{"front-left", "front-right"}, 2},
		{nil, []string{"mono"}, 1},
		{"float32le 8ch 48000Hz", nil, 8},
		{"float32le 9ch 48000Hz", nil, 0},
		{"float32le 0ch 48000Hz", nil, 0},
		{"float32le 2ch 48000Hz", "mono", 0},
		{"nonsense", "mono", 0},
		{nil, nil, 0},
		{nil, "", 0},
		{nil, "left,,right", 0},
		{nil, 2, 0},
		{"s16le 1ch 2ch 48000Hz", nil, 0},
	}
	for _, test := range tests {
		got, err := nativeChannels(jsonBytes(t, test.spec), jsonBytes(t, test.cmap))
		if test.want == 0 {
			if err == nil {
				t.Fatalf("accepted spec %v map %v: %d", test.spec, test.cmap, got)
			}
		} else if err != nil || got != test.want {
			t.Fatalf("spec %v map %v: got %d, %v; want %d", test.spec, test.cmap, got, err, test.want)
		}
	}
}

func outputFixture(pid int, token string, index any) map[string]any {
	return map[string]any{
		"source": index,
		"properties": map[string]any{
			"application.process.id": strconv.Itoa(pid),
			"media.name":             captureStreamName,
			"mv7.meter.id":           token,
		},
	}
}

func TestVerifyOutput(t *testing.T) {
	selected := source{Index: 42}
	for _, test := range []struct {
		name   string
		modify func(map[string]any)
		ok     bool
	}{
		{"correct", func(map[string]any) {}, true},
		{"string index", func(s map[string]any) { s["source"] = "42" }, true},
		{"moved", func(s map[string]any) { s["source"] = 7 }, false},
		{"missing source", func(s map[string]any) { delete(s, "source") }, false},
		{"wrong PID", func(s map[string]any) { s["properties"].(map[string]any)["application.process.id"] = "456" }, false},
		{"wrong stream", func(s map[string]any) { s["properties"].(map[string]any)["media.name"] = "other stream" }, false},
		{"wrong token", func(s map[string]any) { s["properties"].(map[string]any)["mv7.meter.id"] = "other token" }, false},
		{"missing token", func(s map[string]any) { delete(s["properties"].(map[string]any), "mv7.meter.id") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := outputFixture(123, "token", 42)
			test.modify(output)
			err := verifyOutput(jsonBytes(t, []any{output}), selected, 123, "token")
			if (err == nil) != test.ok {
				t.Fatalf("got %v, expected success=%v", err, test.ok)
			}
		})
	}
	output := outputFixture(123, "token", 42)
	for _, data := range [][]byte{
		[]byte("{bad"), []byte("[]"), jsonBytes(t, []any{output, output}),
	} {
		if err := verifyOutput(data, selected, 123, "token"); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
