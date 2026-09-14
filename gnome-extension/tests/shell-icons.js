import Clutter from 'gi://Clutter';
import GdkPixbuf from 'gi://GdkPixbuf';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Shell from 'gi://Shell';
import St from 'gi://St';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import {demoState} from './shell-layout.js';

function assert(condition, message) {
    if (!condition)
        throw new Error(message);
}

function equal(actual, expected, message) {
    assert(JSON.stringify(actual) === JSON.stringify(expected),
        `${message}: ${JSON.stringify(actual)} != ${JSON.stringify(expected)}`);
}

async function frame(ms = 100) {
    await new Promise(resolve => {
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, ms, () => {
            resolve();
            return GLib.SOURCE_REMOVE;
        });
    });
    await new Promise(resolve => {
        const id = global.stage.connect('after-paint', () => {
            global.stage.disconnect(id);
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

async function capture(actor) {
    const box = allocation(actor);
    const stream = Gio.MemoryOutputStream.new_resizable();
    const shot = new Shell.Screenshot();
    await new Promise((resolve, reject) => {
        shot.screenshot_area(Math.floor(box.x), Math.floor(box.y),
            Math.ceil(box.width), Math.ceil(box.height), stream, (object, result) => {
                try {
                    object.screenshot_area_finish(result);
                    resolve();
                } catch (error) {
                    reject(error);
                }
            });
    });
    stream.close(null);
    const bytes = stream.steal_as_bytes();
    const input = Gio.MemoryInputStream.new_from_bytes(bytes);
    const pixels = GdkPixbuf.Pixbuf.new_from_stream(input, null);
    input.close(null);
    return {bytes, pixels};
}

function paintedBounds(background, foreground) {
    equal([background.width, background.height, background.n_channels, background.rowstride],
        [foreground.width, foreground.height, foreground.n_channels, foreground.rowstride],
        'Capture geometry changed while measuring pixels');
    const a = background.get_pixels();
    const b = foreground.get_pixels();
    const channels = foreground.n_channels;
    const stride = foreground.rowstride;
    let x1 = foreground.width, x2 = -1, y1 = foreground.height, y2 = -1, count = 0;
    for (let y = 0; y < foreground.height; y++) {
        for (let x = 0; x < foreground.width; x++) {
            const offset = y * stride + x * channels;
            const contrast = Math.max(...[0, 1, 2].map(c => Math.abs(a[offset + c] - b[offset + c])));
            if (contrast < 12)
                continue;
            x1 = Math.min(x1, x);
            x2 = Math.max(x2, x);
            y1 = Math.min(y1, y);
            y2 = Math.max(y2, y);
            count++;
        }
    }
    return {x: x1, y: y1, width: x2 < x1 ? 0 : x2 - x1 + 1,
        height: y2 < y1 ? 0 : y2 - y1 + 1, pixels: count};
}

function iconInfo(icon) {
    const node = icon.get_theme_node();
    const box = icon.get_allocation_box();
    const local = new Clutter.ActorBox({x1: 0, y1: 0, x2: box.get_width(), y2: box.get_height()});
    const content = node.get_content_box(local);
    return {
        allocated: allocation(icon), explicitSize: icon.icon_size,
        themedSize: node.lookup_length('icon-size', false),
        margins: [icon.margin_top, icon.margin_right, icon.margin_bottom, icon.margin_left],
        preferred: icon.get_preferred_width(-1),
        content: {x: content.x1, y: content.y1, width: content.get_width(), height: content.get_height()},
        children: icon.get_children().map(child => ({
            type: child.constructor.name, allocation: allocation(child),
            preferred: child.get_preferred_width(-1),
        })),
    };
}

async function measure(extension, reference, name) {
    const icon = extension._icon;
    const fill = extension._fillIcon;
    const iconOpacity = icon.opacity, fillOpacity = fill.opacity;
    icon.opacity = 0;
    fill.opacity = 0;
    reference.opacity = 0;
    // Allow asynchronous symbolic texture replacements to settle before capture.
    await frame(500);
    const background = await capture(icon.get_parent());
    const referenceBackground = await capture(reference.get_parent());
    icon.opacity = iconOpacity;
    fill.opacity = fillOpacity;
    reference.opacity = 255;
    await frame();
    const painted = await capture(icon.get_parent());
    const referencePainted = await capture(reference.get_parent());
    assert(iconOpacity === icon.opacity && fillOpacity === fill.opacity, 'Capture changed icon visibility');
    const folder = GLib.getenv('MV7_ICON_OUTPUT');
    if (folder) {
        const evidence = await capture(extension._button.container);
        // Re-encode pixels without screenshot text/provenance chunks.
        evidence.pixels.savev(`${folder}/${name}.png`, 'png', [], []);
    }
    return {
        name,
        panel: allocation(extension._button),
        container: allocation(extension._button.container),
        neighbor: allocation(reference.get_parent().get_parent().container),
        iconBox: allocation(icon.get_parent()),
        referenceBox: allocation(reference.get_parent()),
        outputScale: painted.pixels.width / allocation(icon.get_parent()).width,
        contextScale: St.ThemeContext.get_for_stage(global.stage).scale_factor,
        icon: iconInfo(icon),
        reference: iconInfo(reference),
        painted: paintedBounds(background.pixels, painted.pixels),
        referencePainted: paintedBounds(referenceBackground.pixels, referencePainted.pixels),
    };
}

function verifyGlyph(result, mode) {
    const {painted, referencePainted: stock, outputScale} = result;
    assert(painted.pixels > 0 && stock.pixels > 0, 'Glyph was not painted');
    // Compare microphone/warning silhouettes exactly. The multicolor offline icon's
    // faint edge pixels vary in software rendering; its allocation is checked below.
    if (mode !== 'live' && mode !== 'disconnected') {
        equal([painted.width, painted.height], [stock.width, stock.height],
            `${result.name}: painted glyph must match the stock symbolic icon size`);
        equal([painted.x, painted.y], [stock.x, stock.y],
            `${result.name}: painted glyph must match the stock symbolic icon position`);
    } else if (mode === 'live') {
        // Different outlines may differ by one logical pixel, including theme asymmetry.
        const dx = Math.abs(painted.x + painted.width / 2 - stock.x - stock.width / 2);
        const dy = Math.abs(painted.y + painted.height / 2 - stock.y - stock.height / 2);
        const pixel = outputScale * result.contextScale;
        assert(dx <= pixel && dy <= pixel,
            `${result.name}: live microphone is off-center: ${JSON.stringify({painted, stock, pixel})}`);
        assert(painted.height >= stock.height * 0.8, 'Live microphone became too small');
    }
    equal([result.iconBox.width, result.iconBox.height],
        [result.referenceBox.width, result.referenceBox.height],
        'Icon reservation must match a stock status icon including margins');
    equal([result.icon.allocated.width, result.icon.allocated.height],
        [result.reference.allocated.width, result.reference.allocated.height],
        'Icon must receive the same allocation as a stock status icon');
    equal([result.icon.content.width, result.icon.content.height],
        [result.reference.content.width, result.reference.content.height],
        'Icon content must not be shrunk by margins or padding twice');
}

export async function run(extension) {
    Main.overview.hide();
    extension._button.menu.close();
    extension._settings.set_boolean('meter-enabled', true);
    extension._settings.set_boolean('show-level-numbers', false);
    Main.panel._leftBox.hide();
    Main.panel._centerBox.hide();
    for (const actor of Main.panel._rightBox.get_children()) {
        if (actor !== extension._button.container)
            actor.hide();
    }
    const button = new PanelMenu.Button(0, 'Reference status icon', true);
    const row = new St.BoxLayout({style_class: 'mv7-indicator'});
    const reference = new St.Icon({
        icon_name: 'microphone-sensitivity-muted-symbolic',
        style_class: 'system-status-icon mv7-muted',
    });
    row.add_child(reference);
    button.add_child(row);
    Main.panel.addToStatusArea('mv7-icon-reference', button);
    const settings = new Gio.Settings({schema_id: 'org.gnome.desktop.interface'});
    const scale = settings.get_uint('scaling-factor');
    const contextScale = St.ThemeContext.get_for_stage(global.stage).scale_factor;
    const results = [];
    for (const large of [false, true]) {
        const style = large ? 'icon-size: 20px; padding: 3px 9px 5px 6px; margin: 2px 3px 1px 7px;' : null;
        extension._icon.style = style;
        reference.style = style;
        for (const numbers of [false, true]) {
            extension._settings.set_boolean('show-level-numbers', numbers);
            let expected;
            for (const mode of ['muted', 'live', 'unavailable', 'error', 'disconnected', 'muted']) {
                extension._update({
                    connection: ['error', 'disconnected'].includes(mode) ? mode : 'connected',
                    state: {...demoState, muted: mode === 'muted'},
                });
                extension._updateMeter(mode === 'unavailable' ? null : {
                    available: true, peak_dbfs: -12, rms_dbfs: -21, clipping: false,
                });
                reference.icon_name = extension._icon.icon_name;
                reference.style_class = extension._icon.style_class;
                await frame();
                const name = `${scale}x-${large ? 'padded' : 'normal'}-${numbers ? 'numbers' : 'icon'}-${mode}`;
                const result = await measure(extension, reference, name);
                verifyGlyph(result, mode);
                const geometry = [result.panel, result.container, result.iconBox, result.neighbor];
                if (expected)
                    equal(geometry, expected, 'State transition moved the panel or neighboring actor');
                expected = geometry;
                results.push(result);
            }
        }
    }
    button.destroy();
    return {
        passed: true, scale, contextScale,
        shellTheme: GLib.getenv('MV7_LAYOUT_THEME') ?? 'default',
        iconTheme: settings.get_string('icon-theme'), results,
    };
}
