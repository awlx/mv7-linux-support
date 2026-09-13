package meter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type source struct {
	Index    uint32
	Name     string
	Channels int
}

type pulseSource struct {
	Index               json.RawMessage            `json:"index"`
	Name                string                     `json:"name"`
	MonitorOfSink       json.RawMessage            `json:"monitor_of_sink"`
	MonitorSource       json.RawMessage            `json:"monitor_source"`
	SampleSpecification json.RawMessage            `json:"sample_specification"`
	ChannelMap          json.RawMessage            `json:"channel_map"`
	Properties          map[string]json.RawMessage `json:"properties"`
}

func selectSource(data []byte) (source, error) {
	var sources []pulseSource
	if err := json.Unmarshal(data, &sources); err != nil {
		return source{}, fmt.Errorf("invalid pactl sources JSON: %w", err)
	}
	var matches []pulseSource
	for _, candidate := range sources {
		if !usbID(candidate.Properties["device.vendor.id"], 0x14ed) ||
			!usbID(candidate.Properties["device.product.id"], 0x1019) ||
			strings.HasSuffix(strings.ToLower(candidate.Name), ".monitor") ||
			!nonMonitor(candidate.MonitorOfSink, candidate.MonitorSource) {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) == 0 {
		return source{}, errors.New("no non-monitor Shure MV7+ USB source (14ed:1019) found; refusing default input")
	}
	if len(matches) != 1 {
		return source{}, fmt.Errorf("ambiguous MV7+ input: %d matching USB sources; refusing to choose", len(matches))
	}
	match := matches[0]
	if strings.TrimSpace(match.Name) == "" {
		return source{}, errors.New("MV7+ source has no exact source name")
	}
	index, err := sourceIndex(match.Index)
	if err != nil {
		return source{}, fmt.Errorf("MV7+ source index: %w", err)
	}
	channels, err := nativeChannels(match.SampleSpecification, match.ChannelMap)
	if err != nil {
		return source{}, fmt.Errorf("MV7+ source %q: %w", match.Name, err)
	}
	return source{Index: index, Name: match.Name, Channels: channels}, nil
}

func usbID(raw json.RawMessage, expected uint64) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	number, err := strconv.ParseUint(value, 16, 16)
	return err == nil && number == expected
}

func nonMonitor(sink, monitorSource json.RawMessage) bool {
	// pactl versions use either monitor_source: "" or monitor_of_sink:
	// "n/a"/UINT32_MAX for ordinary inputs. Conflicting fields fail closed.
	known := false
	if len(sink) != 0 {
		known = true
		if !nonMonitorSink(sink) {
			return false
		}
	}
	if len(monitorSource) != 0 {
		known = true
		var name *string
		if json.Unmarshal(monitorSource, &name) != nil || name == nil || *name != "" {
			return false
		}
	}
	return known
}

func nonMonitorSink(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.EqualFold(strings.TrimSpace(value), "n/a") || value == "4294967295"
	}
	var index uint64
	return json.Unmarshal(raw, &index) == nil && string(raw) != "null" && index == 4294967295
}

func sourceIndex(raw json.RawMessage) (uint32, error) {
	value := string(raw)
	var text string
	if json.Unmarshal(raw, &text) == nil {
		value = text
	}
	index, err := strconv.ParseUint(value, 10, 32)
	if err != nil || index == 4294967295 {
		return 0, errors.New("missing or invalid source index")
	}
	return uint32(index), nil
}

func nativeChannels(spec, channelMap json.RawMessage) (int, error) {
	var specCount, mapCount int
	if len(spec) != 0 && string(spec) != "null" {
		var text string
		if json.Unmarshal(spec, &text) == nil {
			for _, field := range strings.Fields(text) {
				if strings.HasSuffix(field, "ch") {
					count, err := strconv.Atoi(strings.TrimSuffix(field, "ch"))
					if err != nil || count < 1 || specCount != 0 {
						return 0, errors.New("invalid native sample specification")
					}
					specCount = count
				}
			}
			if specCount == 0 {
				return 0, errors.New("native sample specification has no channel count")
			}
		} else {
			var object struct {
				Channels int `json:"channels"`
			}
			if json.Unmarshal(spec, &object) != nil || object.Channels < 1 {
				return 0, errors.New("invalid native sample specification")
			}
			specCount = object.Channels
		}
	}
	if len(channelMap) != 0 && string(channelMap) != "null" {
		var text string
		var names []string
		if json.Unmarshal(channelMap, &text) == nil {
			if strings.TrimSpace(text) != "" {
				names = strings.Split(text, ",")
			}
		} else if json.Unmarshal(channelMap, &names) != nil {
			return 0, errors.New("invalid native channel map")
		}
		for _, name := range names {
			if strings.TrimSpace(name) == "" {
				return 0, errors.New("empty channel in native channel map")
			}
		}
		mapCount = len(names)
	}
	if specCount != 0 && mapCount != 0 && specCount != mapCount {
		return 0, errors.New("native sample specification and channel map disagree")
	}
	count := specCount
	if count == 0 {
		count = mapCount
	}
	if count < 1 || count > maxChannels {
		return 0, fmt.Errorf("unsupported native channel count %d (expected 1–%d)", count, maxChannels)
	}
	return count, nil
}

var errStreamPending = errors.New("MV7+ capture stream is not present in pactl source-outputs")

func verifyOutput(data []byte, selected source, pid int, token string) error {
	var outputs []struct {
		Source     json.RawMessage            `json:"source"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &outputs); err != nil {
		return fmt.Errorf("invalid pactl source-outputs JSON: %w", err)
	}
	matches := 0
	for _, output := range outputs {
		var streamToken string
		if json.Unmarshal(output.Properties["mv7.meter.id"], &streamToken) != nil || streamToken != token {
			continue
		}
		matches++
		var processID, streamName string
		if json.Unmarshal(output.Properties["application.process.id"], &processID) != nil ||
			processID != strconv.Itoa(pid) ||
			json.Unmarshal(output.Properties["media.name"], &streamName) != nil ||
			streamName != captureStreamName {
			return errors.New("MV7+ capture stream process/stream identity could not be verified")
		}
		index, err := sourceIndex(output.Source)
		if err != nil {
			return fmt.Errorf("MV7+ capture stream routing: %w", err)
		}
		if index != selected.Index {
			return fmt.Errorf("MV7+ capture stream moved to source %d (expected %d); stopping capture", index, selected.Index)
		}
	}
	if matches == 0 {
		return errStreamPending
	}
	if matches != 1 {
		return errors.New("ambiguous MV7+ capture stream identity")
	}
	return nil
}
