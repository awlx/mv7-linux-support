// Package meter measures the MV7+'s brokered USB audio input, not its HID state.
// It requires pactl and parec from pulseaudio-utils and a PulseAudio-compatible
// server (including PipeWire's PulseAudio service). Audio is reduced in memory
// to scalar dBFS readings and is never stored or forwarded by this package.
//
// Only a unique, non-monitor source advertising USB IDs 14ed:1019 is eligible.
// Capture requests that source's exact name and native channel count, never
// intentionally requests the default input, and never opens an exclusive ALSA
// device. No gain or mute settings are changed.
//
// PipeWire stream properties request that the broker not reconnect, fall back,
// or move the capture stream at runtime: node.dont-reconnect, node.dont-fallback,
// and node.dont-move. node.dont-move asks WirePlumber to ignore target.node and
// target.object metadata moves (the mechanism EasyEffects uses to reroute
// streams), so the capture stays on the uniquely identified MV7+ source.
// Before publishing, and periodically thereafter, the source identity and the
// capture stream's source index, process ID, and unique token are checked.
// A missing, moved, or unverifiable stream stops capture. This is supervision,
// not an atomic wrong-device guarantee: PulseAudio can ignore PipeWire-specific
// properties, and broker routing changes between checks (including a move and
// return) cannot be excluded. Normally checks run every 500 ms; each pactl
// command has a one-second deadline. Consumers must not treat this as a
// security boundary against a broker that changes or falsifies routing.
package meter
