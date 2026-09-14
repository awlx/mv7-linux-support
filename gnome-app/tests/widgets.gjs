import Adw from 'gi://Adw?version=1';
import Cairo from 'cairo';
import GdkPixbuf from 'gi://GdkPixbuf';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Gtk from 'gi://Gtk?version=4.0';
import System from 'system';
import {MV7Application} from '../application.js';
import {CONTROLS} from '../controls.js';

function assert(condition, message) {
    if (!condition)
        throw new Error(message);
}

function pause(milliseconds) {
    return new Promise(resolve => GLib.timeout_add(GLib.PRIORITY_DEFAULT, milliseconds, () => {
        resolve();
        return GLib.SOURCE_REMOVE;
    }));
}

const loop = new GLib.MainLoop(null, false);
let exitCode = 0;
let app;

function checkMeterColors() {
    const directory = GLib.dir_make_tmp('mv7-meter-pixels-XXXXXX');
    const path = GLib.build_filenamev([directory, 'meter.png']);
    const direction = app._meterBar.get_direction();
    const render = () => {
        const surface = new Cairo.ImageSurface(Cairo.Format.ARGB32, 120, 12);
        const cr = new Cairo.Context(surface);
        app._drawMeter(cr, 120, 12);
        cr.$dispose();
        surface.writeToPNG(path);
        const image = GdkPixbuf.Pixbuf.new_from_file(path);
        const data = Uint8Array.from(image.get_pixels());
        const stride = image.get_rowstride();
        const channels = image.get_n_channels();
        return x => Array.from(data.slice(6 * stride + x * channels, 6 * stride + x * channels + 4));
    };
    const isGreen = ([r, g, b]) => g > r && g > b;
    try {
        app._meterBar.set_direction(Gtk.TextDirection.LTR);
        app._applyMeter({available: true, peak_dbfs: 0, rms_dbfs: -3,
            clipping: true, source: 'palette-full'});
        let pixel = render();
        assert(isGreen(pixel(30)), 'Low end of the full-scale gradient must be green');
        let [r, g, b] = pixel(108);
        assert(r > 180 && g > 140 && b < 100, 'Gradient must be yellow near -6 dBFS');
        [r, g, b] = pixel(114);
        assert(r > 1.5 * g && r > 1.5 * b, 'Gradient must approach red near full scale');
        app._applyMeter({available: true, peak_dbfs: -0.1, rms_dbfs: -3,
            clipping: false, source: 'palette-headroom'});
        assert(!app._meterDetail.label.includes('CLIPPING') && !app._meterTitle.has_css_class('error'),
            'A red high-level warning must not claim actual clipping');

        app._applyMeter({available: true, peak_dbfs: -30, rms_dbfs: -33,
            clipping: false, source: 'palette-quiet'});
        pixel = render();
        assert(isGreen(pixel(30)), 'A quiet signal must remain green, not stretch the gradient');
        assert(pixel(90)[3] === 0, 'Unfilled headroom must not be painted as signal');
        app._meterBar.set_direction(Gtk.TextDirection.RTL);
        pixel = render();
        assert(isGreen(pixel(90)) && pixel(30)[3] === 0, 'RTL must reverse the meter direction');

        app._applyMeter({available: false, error: 'Fixture meter unavailable'});
        pixel = render();
        assert(pixel(60)[3] === 0, 'Unavailable input must not retain a signal or peak marker');
    } finally {
        app._meterBar.set_direction(direction);
        const image = Gio.File.new_for_path(path);
        if (image.query_exists(null))
            image.delete(null);
        Gio.File.new_for_path(directory).delete(null);
    }
}

async function checkWidgets() {
    const sent = [];
    let stopped = false;
    app = new MV7Application();
    // Replace the connection boundary before activation: this test never opens a socket.
    app._connectClient = () => {
        app._client = {
            send(action, params) { sent.push({action, params}); return true; },
            stop() { stopped = true; },
            setMeterEnabled() {},
        };
    };
    app.register(null);
    app.activate();
    assert(app._liveRow.get_parent() instanceof Gtk.ListBox &&
        app._liveRow.get_parent().has_css_class('boxed-list'),
        'Live input must use the same boxed preference-row styling as settings');
    assert(app._statusTitle.is_ancestor(app._liveRow) && app._meterBar.is_ancestor(app._liveRow),
        'Microphone status and the complete meter must share the live-input card');
    assert(app._bindings.length === CONTROLS.length, 'All controls must be rendered');
    assert(app._bindings.every(binding => !binding.row.sensitive), 'Offline controls must be disabled');

    const state = {device_name: 'Fixture microphone', firmware: 'fixture', serial: 'fixture'};
    for (const control of CONTROLS) {
        state[control.field] = control.kind === 'switch' ? false :
            control.kind === 'color' ? [178, 255, 51] :
                control.kind === 'choice' ? (control.offset ?? 0) : control.min;
    }
    state.gain_db = 12.5;
    const connected = {connection: 'connected', state, error: null};
    app._applySnapshot(connected);
    await pause(350);
    const mute = app._bindings.find(binding => binding.control.field === 'muted');
    const auto = app._bindings.find(binding => binding.control.field === 'auto_level');
    const gain = app._bindings.find(binding => binding.control.field === 'gain_db');
    const lock = app._bindings.find(binding => binding.control.field === 'gain_locked');
    assert(app._bindings.filter(binding => binding.control.field === 'muted').length === 1,
        'Hardware mute must appear exactly once');
    assert(mute.row.get_next_sibling() === app._liveRow,
        'Mute must be the first row immediately above live input');
    assert(auto.row.get_next_sibling() === gain.row && gain.row.get_next_sibling() === lock.row,
        'Auto Level, manual gain and gain lock must stay together in order');
    const widths = new Set();
    const allocations = [];
    for (const [width, height] of [[520, 600], [1100, 820], [780, 820]]) {
        app._window.set_default_size(width, height);
        await pause(150);
        widths.add(app._window.get_width());
        const settingsCard = gain.row.get_parent();
        const [liveValid, liveBounds] = app._liveRow.get_parent().compute_bounds(app._window);
        const [settingsValid, settingsBounds] = settingsCard.compute_bounds(app._window);
        assert(liveValid && settingsValid &&
            Math.abs(liveBounds.get_x() - settingsBounds.get_x()) < 1 &&
            Math.abs(liveBounds.get_width() - settingsBounds.get_width()) < 1,
            `Live card must match settings width and alignment at window width ${app._window.get_width()}`);
        const [, muteBounds] = mute.row.compute_bounds(app._window);
        const [, meterBounds] = app._liveRow.compute_bounds(app._window);
        const [, autoBounds] = auto.row.compute_bounds(app._window);
        assert(muteBounds.get_y() + muteBounds.get_height() <= meterBounds.get_y() &&
            meterBounds.get_y() + meterBounds.get_height() <= autoBounds.get_y(),
            'Actual native allocation must be Mute, live input, then gain controls');
        allocations.push({
            width: app._window.get_width(), height: app._window.get_height(),
            scale: app._window.get_scale_factor(),
            muteY: muteBounds.get_y(), liveY: meterBounds.get_y(), gainGroupY: autoBounds.get_y(),
            liveWidth: liveBounds.get_width(), settingsWidth: settingsBounds.get_width(),
        });
        mute.widgets[0].grab_focus();
        for (let step = 0; step < 4 && app._window.get_focus() !== auto.widgets[0]; step++) {
            assert(app._window.child_focus(Gtk.DirectionType.TAB_FORWARD), 'Tab navigation from mute failed');
            const focus = app._window.get_focus();
            assert(focus === app._liveRow || focus === auto.row || focus === auto.widgets[0],
                `Tab must pass the readable live card before Auto Level: ${focus}`);
        }
        assert(app._window.get_focus() === auto.widgets[0], 'Auto Level must remain keyboard accessible');
        const last = app._bindings.find(binding => binding.control.field === 'mute_btn_active');
        last.row.grab_focus();
        await pause(200);
        const [, lastBounds] = last.row.compute_bounds(app._window);
        assert(lastBounds.get_y() >= autoBounds.get_y() &&
            lastBounds.get_y() + lastBounds.get_height() <= app._window.get_height() + 1,
            `Keyboard focus must scroll the last Audio control into view: last=${lastBounds.get_y()},${lastBounds.get_height()}, auto=${autoBounds.get_y()}, window=${app._window.get_height()}`);
        auto.widgets[0].grab_focus();
        await pause(200);
    }
    assert(widths.size >= 2, 'Card alignment must be checked at different actual window widths');
    print(`Native control allocations: ${JSON.stringify(allocations)}`);
    assert(app._meterDrawing.get_width() === app._meterBar.get_width() &&
        app._meterDrawing.get_height() === app._meterBar.get_height() &&
        app._meterDrawing.get_height() > 0, 'The drawing overlay must cover the real GTK meter allocation');
    assert(sent.length === 0, 'Programmatic state updates must never send commands');
    assert(app._statusTitle.label === 'Microphone live', 'Live status label must reflect hardware');
    assert(app._bindings.every(binding => binding.row.sensitive), 'Reported controls must be available');
    app._applyMeter({available: true, peak_dbfs: -6, rms_dbfs: -9, clipping: false});
    assert(app._meterTitle.label === 'Input peak: -6 dBFS', 'Native app must show measured input dBFS');
    assert(Math.abs(app._meterBar.value - 54 / 60) < 0.001, 'Meter bar must reflect the measured peak');
    assert(app._peakHoldLabel.label === 'Peak hold: -6 dBFS', 'A new peak must be visible immediately');
    app._applyMeter({available: true, peak_dbfs: 0, rms_dbfs: -3, clipping: true});
    assert(app._meterTitle.has_css_class('error'), 'Clipping must be visible');
    app._applyMeter({available: true, peak_dbfs: -30, rms_dbfs: -33, clipping: false});
    assert(app._meterBar.value === 0.5 && app._meterTitle.label === 'Input peak: -30 dBFS',
        'Live values must continue following the input while a higher peak is held');
    assert(app._peakHoldLabel.label === 'Peak hold: 0 dBFS' &&
        app._peakHoldLabel.has_css_class('error'), 'A recent clipping peak must remain visible');
    assert(!app._meterTitle.has_css_class('error'), 'Held clipping must not imply current clipping');
    await pause(250);
    assert(app._heldPeak.peak_dbfs === 0, 'The peak must not decay during its hold interval');
    await pause(1000);
    assert(app._heldPeak.peak_dbfs < 0 && app._heldPeak.peak_dbfs > -30,
        'The peak timer must decay between readings, without changing the live bar');
    assert(app._meterBar.value === 0.5 && !app._peakHoldLabel.has_css_class('error'),
        'Decay must clear held clipping while preserving the live level');
    const caughtUp = app._heldPeak.peak_dbfs;
    app._applyMeter({available: true, peak_dbfs: caughtUp, rms_dbfs: caughtUp - 3, clipping: false});
    assert(app._peakTimer === 0, 'Animation must stop once the current input catches the marker');
    checkMeterColors();

    app._applyMeter({available: true, peak_dbfs: 0, rms_dbfs: -3, clipping: true, source: 'old-source'});
    app._applyMeter({available: true, peak_dbfs: -40, rms_dbfs: -43, clipping: false, source: 'new-source'});
    assert(app._heldPeak.peak_dbfs === -40 && app._peakTimer === 0, 'Source changes must reset peak history');
    app._settings.set_boolean('meter-enabled', false);
    await pause(50);
    assert(app._peakHoldLabel.label === 'Peak hold: -- dBFS' && app._peakTimer === 0,
        'Disabling metering must clear the held value and remove its timer');
    app._settings.set_boolean('meter-enabled', true);
    await pause(50);

    mute.widgets[0].active = true;
    await pause(350);
    assert(sent.length === 1 && sent[0].action === 'set_mute' && sent[0].params.value === true,
        'Mute switch must send the daemon boolean command');
    assert(app._statusTitle.label === 'Microphone live', 'Status cannot optimistically claim mute');
    app._applySnapshot({...connected, state: {...state, muted: true}});
    assert(app._statusTitle.label === 'Microphone muted', 'Confirmed mute must update the status');

    gain.widgets[1].set_value(13);
    gain.widgets[1].set_value(13.5);
    gain.widgets[1].set_value(14);
    await pause(350);
    assert(sent.length === 2 && sent[1].action === 'set_gain' && sent[1].params.value === 14,
        'Rapid numeric input must coalesce into one correctly quantized command');
    app._applySnapshot({...connected, state: {...state, gain_locked: true}});
    assert(!gain.row.sensitive, 'Hardware gain lock must disable manual gain');
    app._applySnapshot({...connected, state: {...state, auto_level: true}});
    assert(!gain.row.sensitive, 'Auto Level must disable manual gain');

    app._applySnapshot(connected);
    gain.widgets[1].set_value(15);
    app._applySnapshot({connection: 'disconnected', state: null, error: 'Fixture unplugged'});
    await pause(350);
    assert(sent.length === 2, 'Disconnect must cancel pending writes');
    assert(app._bindings.every(binding => !binding.row.sensitive), 'Disconnect must disable all controls');
    assert(app._statusTitle.label === 'Microphone unavailable', 'Stale mute cannot survive disconnect');
    assert(app._meterTitle.label === 'Input peak: -- dBFS' && app._meterBar.value === 0,
        'Disconnect must clear old audio levels, not display false silence');
    assert(app._peakHoldLabel.label === 'Peak hold: -- dBFS' && app._peakTimer === 0,
        'Disconnect must also clear the peak history and animation');

    app._applySnapshot(connected);
    app._confirmReset();
    let dialog = app.get_windows().find(window => window instanceof Adw.MessageDialog);
    assert(dialog, 'Factory reset must require a confirmation dialog');
    dialog.response('cancel');
    await pause(100);
    assert(sent.length === 2, 'Cancelling reset must not send anything');
    app._confirmReset();
    dialog = app.get_windows().find(window => window instanceof Adw.MessageDialog);
    dialog.response('reset');
    assert(sent.at(-1).action === 'factory_reset', 'Explicit reset confirmation must reach the daemon');
    app._applyMeter({available: true, peak_dbfs: -3, rms_dbfs: -6, clipping: false});
    app._applyMeter({available: true, peak_dbfs: -60, rms_dbfs: -63, clipping: false});
    assert(app._peakTimer !== 0, 'A falling peak must have an animation timer');
    app._stopClient();
    assert(stopped && app._pending.size === 0, 'Shutdown must close client and remove timers');
    assert(app._peakTimer === 0 && app._heldPeak === null, 'Shutdown must remove peak animation and history');
    const screenshot = GLib.getenv('MV7_WIDGET_SCREENSHOT');
    if (screenshot) {
        Adw.StyleManager.get_default().set_color_scheme(Adw.ColorScheme.FORCE_DARK);
        app._window.set_default_size(770, 1290);
        app._applySnapshot({...connected, state: {...state, gain_db: 24, hpf: 1, limiter: true,
            compressor: 1, denoiser: true, popper_stopper: true, tone: 0,
            mic_mix: 20, playback_mix: 80, mute_btn_active: true}});
        auto.widgets[0].grab_focus();
        // Let the reset-confirmation toast expire before capturing the normal controls.
        await pause(6000);
        app._applyMeter({available: true, peak_dbfs: -3, rms_dbfs: -21, clipping: false});
        app._applyMeter({available: true, peak_dbfs: -12, rms_dbfs: -21, clipping: false});
        await pause(150);
        const last = app._bindings.find(binding => binding.control.field === 'mute_btn_active');
        const [, lastBounds] = last.row.compute_bounds(app._window);
        assert(lastBounds.get_y() + lastBounds.get_height() <= app._window.get_height(),
            'Screenshot must include the complete Audio page');
        const paintable = Gtk.WidgetPaintable.new(app._window);
        const snapshot = Gtk.Snapshot.new();
        paintable.snapshot(snapshot, app._window.get_width(), app._window.get_height());
        const texture = app._window.get_renderer().render_texture(snapshot.to_node(), null);
        assert(texture.save_to_png(screenshot), 'Could not save native screenshot');
        print(`Native screenshot: ${texture.get_width()}x${texture.get_height()}`);
    }
    print('Native GTK widget checks passed (mocked connection; no hardware commands).');
}

checkWidgets().catch(error => {
    printerr(`${error.message}\n${error.stack ?? ''}`);
    exitCode = 1;
}).finally(() => {
    app?._stopClient();
    for (const window of app?.get_windows() ?? [])
        window.destroy();
    app?.quit();
    loop.quit();
});
loop.run();
System.exit(exitCode);
