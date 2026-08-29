package mv7

// DeviceState is the fully decoded MV7+ state.
type DeviceState struct {
	// Shared features
	GainDB         float64 `json:"gain_db"`         // 0–36 dB
	GainHundredths uint16  `json:"gain_hundredths"` // raw [01 02] value
	GainLocked     bool    `json:"gain_locked"`
	Muted          bool    `json:"muted"`
	HPF            uint8   `json:"hpf"` // 0=off, 1=75Hz, 2=150Hz
	Limiter        bool    `json:"limiter"`
	Compressor     uint8   `json:"compressor"` // 0=off, 1=light, 2=medium, 3=heavy
	Denoiser       bool    `json:"denoiser"`
	AutoLevel      bool    `json:"auto_level"` // 0=manual, 1=auto
	MicMix         uint8   `json:"mic_mix"`    // 0=full mic, 100=full playback
	PopperStopper  bool    `json:"popper_stopper"`
	Tone           int16   `json:"tone"`            // -100..+100 percent
	MuteBtnActive  bool    `json:"mute_btn_active"` // 0=disabled, 1=active (inverted)

	// MV7+ exclusive
	PlaybackMix     uint8 `json:"playback_mix"` // prefix=0x03
	ReverbOutput    bool  `json:"reverb_output"`
	ReverbType      uint8 `json:"reverb_type"`      // 0=Plate, 1=Hall, 2=Studio
	ReverbIntensity uint8 `json:"reverb_intensity"` // 0–100
	ReverbMonitor   bool  `json:"reverb_monitor"`

	// LED panel
	LEDBehavior     uint8    `json:"led_behavior"`   // 0=Live, 1=Pulsing, 2=Solid
	LEDBrightness   uint8    `json:"led_brightness"` // 3=Low, 4=Med, 5=High, 6=Max
	LEDLiveTheme    uint8    `json:"led_live_theme"` // 0=Default, 1=Seaside, 2=Space, 3=Fruity, 4=Custom
	LEDLiveEdge     [3]uint8 `json:"led_live_edge"`  // RGB
	LEDLiveMiddle   [3]uint8 `json:"led_live_middle"`
	LEDLiveInterior [3]uint8 `json:"led_live_interior"`
	LEDSolidColor   [3]uint8 `json:"led_solid_color"`
	LEDPulsingColor [3]uint8 `json:"led_pulsing_color"`
	LEDSolidTheme   uint8    `json:"led_solid_theme"`   // 0=Shure, 1=Custom
	LEDPulsingTheme uint8    `json:"led_pulsing_theme"` // 0=Shure, 1=Custom

	// Identity
	DeviceName string `json:"device_name"`
	Firmware   string `json:"firmware"`
	Serial     string `json:"serial"`
}

// DefaultState returns the factory-default state (matching shurectl defaults).
func DefaultState() DeviceState {
	return DeviceState{
		GainDB:          36,
		HPF:             0,
		Compressor:      0,
		MicMix:          0,
		PopperStopper:   true,
		Tone:            0,
		ReverbType:      0,
		ReverbIntensity: 50,
		LEDBehavior:     0, // Live
		LEDBrightness:   5, // High
		LEDLiveTheme:    0, // Default
		LEDSolidTheme:   0, // Shure
		LEDPulsingTheme: 0, // Shure
		LEDSolidColor:   [3]uint8{0xB2, 0xFF, 0x33},
		LEDPulsingColor: [3]uint8{0x10, 0x3F, 0xFB},
		LEDLiveEdge:     [3]uint8{0xFF, 0xFF, 0xFF},
		LEDLiveMiddle:   [3]uint8{0x1F, 0x1F, 0x1F},
		LEDLiveInterior: [3]uint8{0x00, 0x00, 0x00},
		DeviceName:      "Unknown",
		Firmware:        "Unknown",
		Serial:          "Unknown",
	}
}
