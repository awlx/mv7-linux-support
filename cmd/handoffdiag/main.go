package main

import (
	"fmt"
	"time"

	"github.com/awlx/mv7-linux-support/internal/hidraw"
	"github.com/awlx/mv7-linux-support/internal/mv7"
)

func main() {
	for {
		raw, err := hidraw.Find()
		if err != nil {
			fmt.Printf("%s waiting: %v\n", time.Now().Format(time.RFC3339Nano), err)
			time.Sleep(250 * time.Millisecond)
			continue
		}

		device := mv7.NewDevice(raw)
		for {
			state, err := device.GetState()
			if err != nil {
				fmt.Printf("%s read_error generation=%d: %v\n", time.Now().Format(time.RFC3339Nano), raw.Generation(), err)
				break
			}
			fmt.Printf(
				"%s gain=%.1f raw=%d bytes=%02x%02x auto=%t generation=%d\n",
				time.Now().Format(time.RFC3339Nano),
				state.GainDB,
				state.GainHundredths,
				byte(state.GainHundredths>>8),
				byte(state.GainHundredths),
				state.AutoLevel,
				raw.Generation(),
			)
			time.Sleep(250 * time.Millisecond)
		}
		_ = device.Close()
		time.Sleep(250 * time.Millisecond)
	}
}
