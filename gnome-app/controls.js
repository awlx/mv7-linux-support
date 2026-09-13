export const APP_ID = 'io.github.awlx.MV7';

export const PAGES = [
    {id: 'audio', title: 'Audio', icon: 'audio-input-microphone-symbolic'},
    {id: 'reverb', title: 'Reverb', icon: 'audio-speakers-symbolic'},
    {id: 'lighting', title: 'Lighting', icon: 'display-brightness-symbolic'},
];

export const CONTROLS = [
    {page: 'audio', group: 'Microphone', field: 'muted', action: 'set_mute', title: 'Mute microphone', kind: 'switch'},
    {page: 'audio', group: 'Microphone', field: 'auto_level', action: 'set_auto_level', title: 'Auto Level', subtitle: 'Turn off to adjust gain manually.', kind: 'switch'},
    {page: 'audio', group: 'Microphone', field: 'gain_db', action: 'set_gain', title: 'Gain', subtitle: 'Manual input gain, in decibels.', kind: 'range', min: 0, max: 36, step: 0.5},
    {page: 'audio', group: 'Microphone', field: 'gain_locked', action: 'set_gain_locked', title: 'Gain lock', kind: 'switch'},
    {page: 'audio', group: 'Processing', field: 'hpf', action: 'set_hpf', title: 'High-pass filter', kind: 'choice', options: ['Off', '75 Hz', '150 Hz']},
    {page: 'audio', group: 'Processing', field: 'limiter', action: 'set_limiter', title: 'Limiter', kind: 'switch'},
    {page: 'audio', group: 'Processing', field: 'compressor', action: 'set_compressor', title: 'Compressor', kind: 'choice', options: ['Off', 'Light', 'Medium', 'Heavy']},
    {page: 'audio', group: 'Processing', field: 'denoiser', action: 'set_denoiser', title: 'Denoiser', kind: 'switch'},
    {page: 'audio', group: 'Processing', field: 'popper_stopper', action: 'set_popper_stopper', title: 'Popper Stopper', kind: 'switch'},
    {page: 'audio', group: 'Processing', field: 'tone', action: 'set_tone', title: 'Tone', subtitle: 'Dark (-100), natural (0), or bright (+100).', kind: 'range', min: -100, max: 100, step: 10},
    {page: 'audio', group: 'Monitoring', field: 'mic_mix', action: 'set_mic_mix', title: 'Monitor mix', subtitle: '0% microphone; 100% playback.', kind: 'range', min: 0, max: 100, step: 1},
    {page: 'audio', group: 'Monitoring', field: 'playback_mix', action: 'set_playback_mix', title: 'Playback mix', subtitle: 'Playback mix percentage.', kind: 'range', min: 0, max: 100, step: 1},
    {page: 'audio', group: 'Touch panel', field: 'mute_btn_active', action: 'set_mute_btn', title: 'Physical mute button', subtitle: 'Allow the microphone touch panel to change mute.', kind: 'switch'},
    {page: 'reverb', group: 'Reverb', field: 'reverb_output', action: 'set_reverb_output', title: 'Reverb in output', kind: 'switch'},
    {page: 'reverb', group: 'Reverb', field: 'reverb_monitor', action: 'set_reverb_monitor', title: 'Reverb in monitor', kind: 'switch'},
    {page: 'reverb', group: 'Reverb', field: 'reverb_type', action: 'set_reverb_type', title: 'Type', kind: 'choice', options: ['Plate', 'Hall', 'Studio']},
    {page: 'reverb', group: 'Reverb', field: 'reverb_intensity', action: 'set_reverb_intensity', title: 'Intensity', kind: 'range', min: 0, max: 100, step: 1},
    {page: 'lighting', group: 'LED panel', field: 'led_behavior', action: 'set_led_behavior', title: 'Behavior', kind: 'choice', options: ['Live', 'Pulsing', 'Solid']},
    {page: 'lighting', group: 'LED panel', field: 'led_brightness', action: 'set_led_brightness', title: 'Brightness', kind: 'choice', options: ['Low', 'Medium', 'High', 'Maximum'], offset: 3},
    {page: 'lighting', group: 'Live', field: 'led_live_theme', action: 'set_led_live_theme', title: 'Live theme', kind: 'choice', options: ['Default', 'Seaside', 'Space', 'Fruity', 'Custom']},
    {page: 'lighting', group: 'Live', field: 'led_live_edge', action: 'set_led_live_edge', title: 'Outer color', kind: 'color'},
    {page: 'lighting', group: 'Live', field: 'led_live_middle', action: 'set_led_live_middle', title: 'Middle color', kind: 'color'},
    {page: 'lighting', group: 'Live', field: 'led_live_interior', action: 'set_led_live_interior', title: 'Inner color', kind: 'color'},
    {page: 'lighting', group: 'Solid', field: 'led_solid_theme', action: 'set_led_solid_theme', title: 'Solid theme', kind: 'choice', options: ['Shure', 'Custom']},
    {page: 'lighting', group: 'Solid', field: 'led_solid_color', action: 'set_led_solid_color', title: 'Solid color', kind: 'color'},
    {page: 'lighting', group: 'Pulsing', field: 'led_pulsing_theme', action: 'set_led_pulsing_theme', title: 'Pulsing theme', kind: 'choice', options: ['Shure', 'Custom']},
    {page: 'lighting', group: 'Pulsing', field: 'led_pulsing_color', action: 'set_led_pulsing_color', title: 'Pulsing color', kind: 'color'},
];

export function validValue(control, value) {
    switch (control.kind) {
    case 'switch':
        return typeof value === 'boolean';
    case 'range':
        return Number.isFinite(value) && value >= control.min && value <= control.max;
    case 'choice':
        return Number.isInteger(value) && value >= (control.offset ?? 0) &&
            value < (control.offset ?? 0) + control.options.length;
    case 'color':
        return Array.isArray(value) && value.length === 3 &&
            value.every(channel => Number.isInteger(channel) && channel >= 0 && channel <= 255);
    default:
        return false;
    }
}

export function controlEnabled(control, snapshot) {
    const state = snapshot?.state;
    return snapshot?.connection === 'connected' && state !== null &&
        typeof state?.muted === 'boolean' && validValue(control, state[control.field]) &&
        (control.field !== 'gain_db' || (state.gain_locked === false && state.auto_level === false));
}

export function commandFor(control, value) {
    if (!validValue(control, value))
        throw new Error(`Invalid value for ${control.title}`);
    if (control.kind === 'range') {
        value = Math.round(value / control.step) * control.step;
        value = Math.min(control.max, Math.max(control.min, value));
    }
    return {action: control.action, params: control.kind === 'color' ? {rgb: value} : {value}};
}
