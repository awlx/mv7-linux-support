import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Pango from 'gi://Pango';
import Shell from 'gi://Shell';
import St from 'gi://St';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import {meterDisplay} from '../state.js';

export const demoState = {
    muted: false, gain_db: 24, gain_locked: false, auto_level: false,
    hpf: 1, limiter: true, compressor: 1, denoiser: true,
    popper_stopper: true, tone: 0, mic_mix: 20,
};
const painted = new WeakMap();

function assert(condition, message) {
    if (!condition)
        throw new Error(message);
}

function equal(actual, expected, message) {
    assert(JSON.stringify(actual) === JSON.stringify(expected),
        `${message}: ${JSON.stringify(actual)} != ${JSON.stringify(expected)}`);
}

async function delay(ms = 40) {
    await new Promise(resolve => {
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, ms, () => {
            resolve();
            return GLib.SOURCE_REMOVE;
        });
    });
    await new Promise(resolve => {
        const handler = global.stage.connect('after-paint', () => {
            global.stage.disconnect(handler);
            resolve();
        });
        global.stage.queue_redraw();
    });
}

function allocation(actor) {
    const [x, y] = actor.get_transformed_position();
    const box = actor.get_allocation_box();
    return {x, y, width: box.get_width(), height: box.get_height()};
}

function reading(peak, rms = peak) {
    return peak === null ? null : {
        available: true, peak_dbfs: peak, rms_dbfs: rms, clipping: peak === 0,
    };
}

function textFits(label) {
    const text = label.clutter_text;
    const layout = text.get_layout();
    const [width, height] = layout.get_pixel_size();
    const box = text.get_allocation_box();
    assert(!layout.is_ellipsized(), `Ellipsized: ${label.text}`);
    assert(width <= box.get_width() && height <= box.get_height(),
        `Text does not fit ${JSON.stringify(label.text)}: ${width}x${height} in ${box.get_width()}x${box.get_height()}`);
}

function dimensions(extension, before, after) {
    return {
        panel: allocation(extension._button),
        container: allocation(extension._button.container),
        before: allocation(before),
        after: allocation(after),
        icon: allocation(extension._fillIcon.get_parent()),
        labelWidth: extension._levelLabel.visible ? allocation(extension._levelLabel).width : 0,
        popupWidth: allocation(extension._button.menu.actor).width,
        titleWidth: allocation(extension._meterTitle).width,
        barX: extension._meterBar.get_allocation_box().x1,
        barStageX: allocation(extension._meterBar).x,
        barWidth: allocation(extension._meterBar).width,
    };
}

function checkContent(extension, meterEnabled, numbers) {
    const fields = ['muted', 'auto_level', 'gain_db', 'gain_locked'];
    const [mute, auto, gain, lock] = fields.map(field => {
        const matches = extension._bindings.filter(binding => binding.field === field);
        equal(matches.length, 1, `Exactly one ${field} control`);
        return matches[0].item;
    });
    const rows = [mute, extension._meterRow, extension._meterDetail, extension._meterSwitch, auto, gain, lock];
    for (let i = 1; i < rows.length; i++) {
        const prior = allocation(rows[i - 1]), next = allocation(rows[i]);
        assert(prior.y + prior.height <= next.y,
            'Actual popup allocation must be Mute, live meter, then gain controls');
    }
    equal(extension._levelLabel.visible, meterEnabled && numbers, 'Numeric preference');
    if (extension._levelLabel.visible)
        textFits(extension._levelLabel);
    textFits(extension._meterTitle);
    textFits(extension._status.label);
    textFits(extension._meterDetail.label);
    if (extension._error.visible)
        textFits(extension._error.label);
    const showFill = meterEnabled && extension._meterReading?.available === true &&
        extension._snapshot.connection === 'connected' && extension._snapshot.state?.muted === false;
    equal(extension._fillIcon.opacity, showFill ? 255 : 0, 'Fill priority');
    equal(extension._icon.opacity, showFill ? 0 : 255, 'Hardware icon priority');
    if (showFill) {
        const [width, height] = painted.get(extension._fillIcon) ?? [0, 0];
        assert(width > 0 && height > 0, `Empty input glyph surface: ${width}x${height}`);
    }
    assert(extension._button.accessible_name.includes(extension._status.label.text),
        'Hardware status missing from accessible name');
    assert(extension._meterBar.accessible_name.includes('MV7+ input level:'), 'Meter accessible name');
    if (meterEnabled)
        assert(extension._button.accessible_name.includes(extension._levelLabel.text), 'Accessible level');
    assert(allocation(extension._meterBar).width > 0, 'Meter has no usable width');
    const menu = allocation(extension._button.menu.actor);
    assert(menu.width <= Main.layoutManager.primaryMonitor.width, 'Menu wider than monitor');
    assert(menu.height <= Main.layoutManager.primaryMonitor.height, 'Menu taller than monitor');
}

async function screenshot(extension) {
    const path = GLib.getenv('MV7_LAYOUT_SCREENSHOT');
    if (!path)
        return;
    const hidden = [Main.panel._leftBox, Main.panel._centerBox,
        ...Main.panel._rightBox.get_children().filter(actor => actor !== extension._button.container)];
    const visibility = hidden.map(actor => actor.visible);
    hidden.forEach(actor => actor.hide());
    const context = St.ThemeContext.get_for_stage(global.stage);
    const scale = context.scale_factor;
    const panelStyle = Main.panel.style;
    context.scale_factor = 2;
    Main.panel.style = 'font-size: 11pt;';
    extension._button.menu.box.style = 'font-size: 11pt;';
    extension._update({connection: 'connected', state: demoState});
    extension._settings.set_boolean('meter-enabled', true);
    extension._settings.set_boolean('show-level-numbers', true);
    extension._updateMeter(reading(-12, -21));
    extension._button.menu.close();
    extension._button.menu.open();
    await delay(300);
    const menu = allocation(extension._button.menu.actor);
    const panel = allocation(extension._button);
    const x = Math.max(0, Math.floor(Math.min(menu.x, panel.x) - 8));
    const right = Math.min(global.stage.width, Math.ceil(Math.max(menu.x + menu.width, panel.x + panel.width) + 8));
    const bottom = Math.min(global.stage.height, Math.ceil(menu.y + menu.height + 8));
    const stream = Gio.File.new_for_path(path).replace(null, false, Gio.FileCreateFlags.NONE, null);
    const metrics = {
        menu, panel,
        surface: painted.get(extension._fillIcon),
        icon: allocation(extension._fillIcon),
        panelColor: extension._button.get_theme_node().get_foreground_color().to_string(),
        labelColor: extension._levelLabel.get_theme_node().get_foreground_color().to_string(),
        fillColor: extension._fillIcon.get_theme_node().get_foreground_color().to_string(),
        panelOpacity: Main.panel.get_paint_opacity(),
        buttonOpacity: extension._button.get_paint_opacity(),
        fillOpacity: extension._fillIcon.get_paint_opacity(),
    };
    const capture = new Shell.Screenshot();
    await new Promise((resolve, reject) => {
        capture.screenshot_area(x, 0, right - x, bottom, stream, (object, result) => {
            try {
                object.screenshot_area_finish(result);
                resolve();
            } catch (error) {
                reject(error);
            }
        });
    });
    stream.close(null);
    hidden.forEach((actor, index) => { actor.visible = visibility[index]; });
    context.scale_factor = scale;
    Main.panel.style = panelStyle;
    extension._button.menu.box.style = null;
    return metrics;
}

async function checkControls(extension) {
    const sent = [];
    extension._client = {send: (action, params) => { sent.push({action, params}); return true; }};
    const mute = extension._bindings.find(binding => binding.field === 'muted');
    const gain = extension._bindings.find(binding => binding.field === 'gain_db');
    extension._update({connection: 'connected', state: demoState});
    mute.item.activate({type: () => Clutter.EventType.KEY_PRESS, get_key_symbol: () => Clutter.KEY_space});
    equal(sent, [{action: 'set_mute', params: {value: true}}], 'Hardware mute command');
    equal(extension._status.label.text, 'MV7+ microphone live', 'Mute must wait for hardware confirmation');
    extension._update({connection: 'connected', state: {...demoState, muted: true}});
    equal(extension._status.label.text, 'MV7+ microphone muted', 'Confirmed hardware mute');
    for (const flags of [{auto_level: true}, {gain_locked: true}]) {
        extension._update({connection: 'connected', state: {...demoState, ...flags}});
        assert(!gain.slider.reactive && !gain.slider.can_focus, 'Locked/automatic gain must stay disabled');
        extension._send(gain, 15);
        equal(sent.length, 1, 'Disabled gain must not send a command');
    }
    extension._update({connection: 'connected', state: demoState});
    extension._debounce(gain, 15);
    extension._update({connection: 'disconnected', state: null});
    await delay(250);
    equal(sent.length, 1, 'Disconnect must cancel pending gain writes');
    equal(extension._pending.size, 0, 'Disconnect must clear pending writes');
    assert(!mute.item.reactive, 'Disconnected mute must be disabled');
    extension._client = null;
    extension._update({connection: 'connected', state: demoState});
}

export async function run(extension) {
    Main.overview.hide();
    extension._fillIcon.connect('repaint', actor => painted.set(actor, actor.get_surface_size()));
    assert(extension._bindings.length === 11, 'Test must use the complete production controls');
    assert(extension._settings.get_default_value('show-level-numbers').get_boolean() === false,
        'Numbers must remain off by default');
    const before = new St.Label({text: 'Before'});
    const after = new St.Label({text: 'After'});
    const parent = extension._button.container.get_parent();
    parent.insert_child_below(before, extension._button.container);
    parent.insert_child_above(after, extension._button.container);
    const context = St.ThemeContext.get_for_stage(global.stage);
    const originalFont = context.get_font().copy();
    const originalScale = context.scale_factor;
    const settings = extension._settings;
    const measurements = [];
    let samples = 0;
    extension._button.menu.open();
    await delay(500);
    await checkControls(extension);
    for (const [scale, large] of [[1, false], [2, false], [2, true], [1, true], [1, false]]) {
        context.scale_factor = scale;
        context.set_font(large ? Pango.FontDescription.from_string('DejaVu Serif 15') : originalFont);
        const font = large ? 'font-family: DejaVu Serif; font-size: 18px;' : null;
        extension._button.style = font;
        extension._button.menu.box.style = font;
        const iconStyle = large ? 'icon-size: 20px; padding: 4px 9px;' : null;
        extension._icon.style = iconStyle;
        extension._button.menu.close();
        extension._button.menu.open();
        await delay(200);
        for (const [enabled, numbers] of [[true, true], [true, false], [false, true]]) {
            extension._update({connection: 'connected', state: demoState});
            settings.set_boolean('meter-enabled', enabled);
            settings.set_boolean('show-level-numbers', numbers);
            extension._updateMeter(reading(-12));
            await delay(150);
            const expected = dimensions(extension, before, after);
            const menuHeight = allocation(extension._button.menu.actor).height;
            const switchY = allocation(extension._meterSwitch).y;
            const check = async description => {
                await delay();
                equal(dimensions(extension, before, after), expected,
                    `Allocation changed (${scale}x, large=${large}, meter=${enabled}, numbers=${numbers}, ${description})`);
                checkContent(extension, enabled, numbers);
                samples++;
            };
            for (const peak of [0, -9, -10, -89, -90, -89.9, null, -71, -63, -69, 0, null, -12.34]) {
                extension._updateMeter(reading(peak));
                await check(`peak ${peak}`);
                equal(allocation(extension._button.menu.actor).height, menuHeight, 'Steady meter menu height');
                equal(allocation(extension._meterSwitch).y, switchY, 'Steady settings row position');
            }
            for (const error of [
                'No MV7+ input source',
                'Capture unavailable. Check that the microphone is connected and PulseAudio or PipeWire is running.',
                'x'.repeat(180),
            ]) {
                extension._updateMeter({available: false, error});
                await check('unavailable detail');
            }
            for (const [connection, muted] of [
                ['connected', true], ['connected', false], ['error', null],
                ['disconnected', null], ['connecting', null], ['connected', false],
            ]) {
                extension._update({
                    connection, state: muted === null ? null : {...demoState, muted},
                    error: connection === 'error' ? 'Device state unavailable. Please reconnect the microphone.' : null,
                });
                if (connection === 'connected')
                    extension._updateMeter(reading(-9));
                await check(`${connection}, muted=${muted}`);
                const icon = connection === 'connected'
                    ? muted ? 'microphone-sensitivity-muted-symbolic' : 'audio-input-microphone-symbolic'
                    : connection === 'error' ? 'dialog-warning-symbolic' : 'network-offline-symbolic';
                equal(extension._icon.icon_name, icon, 'Hardware state icon');
            }
            measurements.push({scale, large, enabled, numbers, ...expected});
        }
        settings.set_boolean('meter-enabled', true);
        settings.set_boolean('show-level-numbers', true);
        extension._update({connection: 'connected', state: demoState});
        extension._updateMeter(reading(0));
        await delay(100);
        const expected = dimensions(extension, before, after);
        for (let peak = 0; peak >= -90; peak--) {
            extension._updateMeter(reading(peak));
            await delay(20);
            equal(dimensions(extension, before, after), expected, `Full integer range at ${peak}`);
            checkContent(extension, true, true);
            equal(extension._levelLabel.text, meterDisplay(reading(peak)).text, 'Unchanged dBFS value');
            samples++;
        }
        assert(GObject.signal_lookup('queue-relayout', Clutter.Actor.$gtype), 'Relayout signal unavailable');
        let relayouts = 0;
        const handler = extension._button.connect('queue-relayout', () => relayouts++);
        await delay(200);
        equal(relayouts, 0, 'Idle relayout loop');
        extension._button.disconnect(handler);
        const scroll = extension._button.menu.box.get_parent();
        const mute = extension._bindings.find(binding => binding.field === 'muted').item;
        const auto = extension._bindings.find(binding => binding.field === 'auto_level').item;
        mute.grab_key_focus();
        assert(extension._button.menu.box.navigate_focus(mute, St.DirectionType.TAB_FORWARD, false),
            'Keyboard navigation from mute failed');
        assert(global.stage.get_key_focus() === extension._meterSwitch, 'Meter toggle follows hardware mute');
        assert(extension._button.menu.box.navigate_focus(extension._meterSwitch, St.DirectionType.TAB_FORWARD, false),
            'Keyboard navigation from meter toggle failed');
        assert(global.stage.get_key_focus() === auto, 'Auto Level follows the live input controls');
        const lastItem = extension._button.menu.box.get_last_child();
        lastItem.grab_key_focus();
        await delay(200);
        const viewport = allocation(scroll);
        const focused = allocation(lastItem);
        assert(focused.y >= viewport.y && focused.y + focused.height <= viewport.y + viewport.height,
            'Keyboard focus did not scroll the last menu item into view');
        mute.grab_key_focus();
        await delay(200);
        const muteBox = allocation(mute);
        assert(muteBox.y >= viewport.y && muteBox.y + muteBox.height <= viewport.y + viewport.height,
            'Keyboard focus did not return the first mute control to view');
        const adjustment = scroll.get_vadjustment
            ? scroll.get_vadjustment() : scroll.get_vscroll_bar().get_adjustment();
        adjustment.value = 0;
        await delay();
    }
    before.destroy();
    after.destroy();
    context.scale_factor = originalScale;
    context.set_font(originalFont);
    extension._button.style = null;
    extension._button.menu.box.style = null;
    extension._icon.style = null;
    extension._fillIcon.style = null;
    const screenshotMetrics = await screenshot(extension);
    // Destroying the actors must release their font/attribute signal bindings.
    extension.disable();
    settings.set_boolean('meter-enabled', false);
    context.set_font(Pango.FontDescription.from_string('Sans 13'));
    await delay();
    context.set_font(originalFont);
    extension.enable();
    await delay();
    assert(extension._button, 'Indicator did not re-enable');
    return {passed: true, samples, measurements, screenshotMetrics};
}
