package meter

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	sampleRate     = 48000
	framesPerChunk = 4800
	maxChannels    = 8
	floorDBFS      = -90.0
)

type pcmResult struct {
	reading   Reading
	completed time.Time
	err       error
}

func reducePCM(data []byte, channels int) (Reading, error) {
	if channels < 1 || channels > maxChannels {
		return Reading{}, fmt.Errorf("unsupported channel count %d (expected 1–%d)", channels, maxChannels)
	}
	if len(data) != framesPerChunk*channels*4 {
		return Reading{}, errors.New("PCM reduction requires exactly 4800 complete frames")
	}
	var peak, squares float64
	for offset := 0; offset < len(data); offset += 4 {
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+4])))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return Reading{}, errors.New("non-finite float32 PCM sample (NaN or infinity)")
		}
		peak = math.Max(peak, math.Abs(value))
		squares += value * value
	}
	peakDBFS := toDBFS(peak)
	rmsDBFS := toDBFS(math.Sqrt(squares / float64(len(data)/4)))
	return Reading{
		Available: true,
		PeakDBFS:  &peakDBFS,
		RMSDBFS:   &rmsDBFS,
		Clipping:  peak >= 1,
	}, nil
}

func toDBFS(amplitude float64) float64 {
	if amplitude <= 0 {
		return floorDBFS
	}
	return math.Max(floorDBFS, math.Min(0, 20*math.Log10(amplitude)))
}

func readPCM(ctx context.Context, reader io.Reader, channels int, results chan<- pcmResult) {
	if channels < 1 || channels > maxChannels {
		sendPCM(ctx, results, pcmResult{err: fmt.Errorf("unsupported channel count %d", channels)})
		return
	}
	data := make([]byte, framesPerChunk*channels*4)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := io.ReadFull(reader, data)
		if err != nil {
			switch {
			case n%4 != 0:
				err = fmt.Errorf("truncated terminal PCM float: %w", err)
			case n%(channels*4) != 0:
				err = fmt.Errorf("truncated terminal PCM frame: %w", err)
			case n != 0:
				err = fmt.Errorf("incomplete terminal PCM chunk (%d of %d frames): %w", n/(channels*4), framesPerChunk, err)
			default:
				err = fmt.Errorf("PCM input ended: %w", err)
			}
			sendPCM(ctx, results, pcmResult{err: err})
			return
		}
		reading, err := reducePCM(data, channels)
		if !sendPCM(ctx, results, pcmResult{reading: reading, completed: time.Now(), err: err}) || err != nil {
			return
		}
	}
}

func sendPCM(ctx context.Context, results chan<- pcmResult, result pcmResult) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}
