package meter

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func pcmBytes(channels int, sample func(frame, channel int) float32) []byte {
	data := make([]byte, framesPerChunk*channels*4)
	for frame := 0; frame < framesPerChunk; frame++ {
		for channel := 0; channel < channels; channel++ {
			offset := (frame*channels + channel) * 4
			binary.LittleEndian.PutUint32(data[offset:offset+4], math.Float32bits(sample(frame, channel)))
		}
	}
	return data
}

func TestReducePCM(t *testing.T) {
	tests := []struct {
		name     string
		channels int
		sample   func(int, int) float32
		peak     float64
		rms      float64
		clipping bool
	}{
		{"constant half", 1, func(int, int) float32 { return .5 }, -6.020599913, -6.020599913, false},
		{"sine half", 1, func(frame, _ int) float32 {
			return float32(.5 * math.Sin(2*math.Pi*1000*float64(frame)/sampleRate))
		}, -6.020599913, -9.030899870, false},
		{"silence", 1, func(int, int) float32 { return 0 }, -90, -90, false},
		{"tiny input", 1, func(int, int) float32 { return 1e-8 }, -90, -90, false},
		{"positive clipping", 1, func(int, int) float32 { return 1 }, 0, 0, true},
		{"negative clipping", 1, func(int, int) float32 { return -1 }, 0, 0, true},
		{"over range", 1, func(int, int) float32 { return 2 }, 0, 0, true},
		{"stereo opposite phase", 2, func(_, channel int) float32 {
			if channel == 0 {
				return .5
			}
			return -.5
		}, -6.020599913, -6.020599913, false},
		{"stereo one active", 2, func(_, channel int) float32 {
			if channel == 0 {
				return .5
			}
			return 0
		}, -6.020599913, -9.030899870, false},
		{"eight channels", 8, func(int, int) float32 { return .5 }, -6.020599913, -6.020599913, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reading, err := reducePCM(pcmBytes(test.channels, test.sample), test.channels)
			if err != nil {
				t.Fatal(err)
			}
			if !reading.Available || reading.PeakDBFS == nil || reading.RMSDBFS == nil {
				t.Fatalf("invalid reading: %+v", reading)
			}
			if math.Abs(*reading.PeakDBFS-test.peak) > 1e-5 || math.Abs(*reading.RMSDBFS-test.rms) > 1e-5 {
				t.Fatalf("got peak %.9f RMS %.9f, want %.9f %.9f", *reading.PeakDBFS, *reading.RMSDBFS, test.peak, test.rms)
			}
			if reading.Clipping != test.clipping {
				t.Fatalf("clipping = %v, want %v", reading.Clipping, test.clipping)
			}
		})
	}
}

func TestReducePCMRejectsNonFinite(t *testing.T) {
	for _, value := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		data := pcmBytes(1, func(int, int) float32 { return .5 })
		binary.LittleEndian.PutUint32(data[len(data)-4:], math.Float32bits(value))
		if _, err := reducePCM(data, 1); err == nil || !strings.Contains(err.Error(), "non-finite") {
			t.Fatalf("expected explicit non-finite error, got %v", err)
		}
	}
}

func TestReducePCMRejectsInvalidSize(t *testing.T) {
	for _, channels := range []int{-1, 0, 9} {
		if _, err := reducePCM(nil, channels); err == nil {
			t.Fatalf("accepted %d channels", channels)
		}
	}
	for _, size := range []int{0, 1, 4, framesPerChunk*4 - 4, framesPerChunk*4 + 4} {
		if _, err := reducePCM(make([]byte, size), 1); err == nil {
			t.Fatalf("accepted %d bytes", size)
		}
	}
}

type fragmentedReader struct {
	*bytes.Reader
}

func (r fragmentedReader) Read(data []byte) (int, error) {
	return r.Reader.Read(data[:min(3, len(data))])
}

func TestReadPCMChunkBoundaries(t *testing.T) {
	half := pcmBytes(2, func(int, int) float32 { return .5 })
	quarter := pcmBytes(2, func(int, int) float32 { return .25 })
	data := append(half, quarter...)
	results := make(chan pcmResult, 3)
	readPCM(context.Background(), fragmentedReader{bytes.NewReader(data)}, 2, results)
	for _, expected := range []float64{-6.020599913, -12.041199827} {
		result := <-results
		if result.err != nil || math.Abs(*result.reading.PeakDBFS-expected) > 1e-5 {
			t.Fatalf("unexpected chunk: %+v", result)
		}
	}
	if result := <-results; result.err == nil || !strings.Contains(result.err.Error(), "PCM input ended") {
		t.Fatalf("expected terminal EOF, got %+v", result)
	}
}

func TestReadPCMRejectsTerminalTruncation(t *testing.T) {
	for _, test := range []struct {
		bytes int
		error string
	}{
		{1, "truncated terminal PCM float"},
		{3, "truncated terminal PCM float"},
		{4, "truncated terminal PCM frame"},
		{8, "incomplete terminal PCM chunk"},
		{framesPerChunk*8 - 8, "incomplete terminal PCM chunk"},
	} {
		results := make(chan pcmResult, 1)
		readPCM(context.Background(), bytes.NewReader(make([]byte, test.bytes)), 2, results)
		result := <-results
		if result.reading.Available || result.err == nil || !strings.Contains(result.err.Error(), test.error) {
			t.Fatalf("%d bytes: got %+v, want %q", test.bytes, result, test.error)
		}
	}
}

func TestReadPCMCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readPCM(ctx, bytes.NewReader(pcmBytes(1, func(int, int) float32 { return .5 })), 1, make(chan pcmResult))
}
