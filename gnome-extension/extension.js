import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import St from 'gi://St';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';
import {Slider} from 'resource:///org/gnome/shell/ui/slider.js';
import {BarLevel} from 'resource:///org/gnome/shell/ui/barLevel.js';

import {DaemonClient} from './daemonClient.js';
import {meterDisplay, parseEndpoint} from './state.js';
import {IconBox, LevelLabel, WrappingLabel} from './panelWidgets.js';

export default class MV7Extension extends Extension {
    enable() {
        this._generation = 0;
        this._bindings = [];
        this._pending = new Map();
        this._snapshot = {connection: 'connecting', state: null, error: null};
        this._applying = false;
        this._settings = this.getSettings();
        this._button = new PanelMenu.Button(0, 'MV7+ Control');
        const menuBox = this._button.menu.box;
        const menuBin = menuBox.get_parent();
        menuBin.remove_child(menuBox);
        const scroll = new St.ScrollView({
            hscrollbar_policy: St.PolicyType.NEVER,
            vscrollbar_policy: St.PolicyType.AUTOMATIC,
            overlay_scrollbars: true,
        });
        scroll.set_child(menuBox);
        menuBin.set_child(scroll);
        this._button.menu.connectObject('active-changed', (_menu, item) => {
            if (!item || !menuBox.contains(item))
                return;
            let top = 0;
            for (let actor = item; actor !== menuBox; actor = actor.get_parent())
                top += actor.get_allocation_box().y1;
            const adjustment = scroll.get_vadjustment
                ? scroll.get_vadjustment() : scroll.get_vscroll_bar().get_adjustment();
            adjustment.clamp_page(top, top + item.get_allocation_box().get_height());
        }, scroll);
        this._icon = new St.Icon({
            icon_name: 'network-offline-symbolic', style_class: 'system-status-icon',
        });
        this._fillIcon = new St.DrawingArea({
            style_class: 'mv7-input-fill',
        });
        const microphone = new IconBox(this._icon, this._fillIcon);
        this._fillIcon.connect('repaint', () => this._drawInputIcon());
        const indicator = new St.BoxLayout({style_class: 'mv7-indicator'});
        this._levelLabel = new LevelLabel({
            text: '-- dBFS', style_class: 'mv7-panel-level', y_align: Clutter.ActorAlign.CENTER,
        });
        indicator.add_child(microphone);
        indicator.add_child(this._levelLabel);
        this._button.add_child(indicator);
        this._status = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        this._status.label = new WrappingLabel({
            text: 'Connecting to MV7+', style_class: 'mv7-status-label',
        });
        this._status.add_child(this._status.label);
        this._status.label_actor = this._status.label;
        this._button.menu.addMenuItem(this._status);
        this._error = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        this._error.label = new WrappingLabel({style_class: 'mv7-error-label'});
        this._error.add_child(this._error.label);
        this._error.label_actor = this._error.label;
        this._error.visible = false;
        this._button.menu.addMenuItem(this._error);
        this._meterRow = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        this._meterTitle = new LevelLabel({text: 'Input peak: -- dBFS'}, 'Input peak: ');
        this._meterBar = new BarLevel({style_class: 'slider mv7-meter-bar', x_expand: true});
        this._meterBar.overdrive_start = 54 / 60;
        this._meterBar.accessible_name = 'MV7+ input peak level';
        const meterBox = new St.BoxLayout({vertical: true, x_expand: true});
        meterBox.add_child(this._meterTitle);
        meterBox.add_child(this._meterBar);
        this._meterRow.add_child(meterBox);
        this._button.menu.addMenuItem(this._meterRow);
        this._meterDetail = new PopupMenu.PopupBaseMenuItem({reactive: false, can_focus: false});
        this._meterDetail.label = new WrappingLabel({
            text: 'Waiting for input level', style_class: 'mv7-meter-detail',
        }, 2);
        this._meterDetail.add_child(this._meterDetail.label);
        this._meterDetail.label_actor = this._meterDetail.label;
        this._button.menu.addMenuItem(this._meterDetail);
        this._meterSwitch = new PopupMenu.PopupSwitchMenuItem(
            'Live input meter', this._settings.get_boolean('meter-enabled'));
        this._meterSwitch.connect('toggled', (_item, value) =>
            this._settings.set_boolean('meter-enabled', value));
        this._button.menu.addMenuItem(this._meterSwitch);
        this._button.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());

        this._switch(this._button.menu, 'Mute microphone', 'muted', 'set_mute');
        this._switch(this._button.menu, 'Auto Level', 'auto_level', 'set_auto_level');
        this._range(this._button.menu, 'Gain', 'gain_db', 'set_gain', 0, 36, 0.5, ' dB');
        this._switch(this._button.menu, 'Gain lock', 'gain_locked', 'set_gain_locked');
        const processing = new PopupMenu.PopupSubMenuMenuItem('Processing');
        this._button.menu.addMenuItem(processing);
        this._choice(processing.menu, 'High-pass filter', 'hpf', 'set_hpf', ['Off', '75 Hz', '150 Hz']);
        this._switch(processing.menu, 'Limiter', 'limiter', 'set_limiter');
        this._choice(processing.menu, 'Compressor', 'compressor', 'set_compressor',
            ['Off', 'Light', 'Medium', 'Heavy']);
        this._switch(processing.menu, 'Denoiser', 'denoiser', 'set_denoiser');
        this._switch(processing.menu, 'Popper Stopper', 'popper_stopper', 'set_popper_stopper');
        this._range(processing.menu, 'Tone', 'tone', 'set_tone', -100, 100, 10, '');
        this._range(this._button.menu, 'Monitor mix', 'mic_mix', 'set_mic_mix', 0, 100, 1, '%');
        this._button.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._button.menu.addAction('Open MV7+ Control', () => this._openApp());
        this._button.menu.addAction('Open web console', () => this._openWeb());
        this._button.menu.addAction('Preferences', () => this.openPreferences());

        Main.panel.addToStatusArea(this.uuid, this._button);
        this._settingsChanged = this._settings.connect('changed::daemon-url', () => this._connect());
        this._meterSettingsChanged = this._settings.connect('changed::meter-enabled', () => {
            const enabled = this._settings.get_boolean('meter-enabled');
            this._meterSwitch.setToggleState(enabled);
            this._client?.setMeterEnabled(enabled);
            this._updateMeter(null);
        });
        this._numberSettingsChanged = this._settings.connect('changed::show-level-numbers', () =>
            this._updateMeter(this._meterReading));
        this._connect();
    }

    _switch(menu, title, field, action) {
        const item = new PopupMenu.PopupSwitchMenuItem(title, false);
        const binding = {item, field, action, valid: value => typeof value === 'boolean',
            apply: value => item.setToggleState(value)};
        item.connect('toggled', (_item, value) => {
            if (!this._applying)
                this._send(binding, value);
        });
        menu.addMenuItem(item);
        this._bindings.push(binding);
    }

    _choice(menu, title, field, action, options) {
        const item = new PopupMenu.PopupSubMenuMenuItem(title);
        const entries = options.map((label, value) => item.menu.addAction(label, () => {
            this._send(binding, value);
        }));
        const binding = {
            item, field, action,
            valid: value => Number.isInteger(value) && value >= 0 && value < options.length,
            apply: value => {
                item.label.text = `${title}: ${options[value]}`;
                entries.forEach((entry, index) => entry.setOrnament(
                    index === value ? PopupMenu.Ornament.DOT : PopupMenu.Ornament.NONE));
            },
        };
        menu.addMenuItem(item);
        this._bindings.push(binding);
    }

    _range(menu, title, field, action, min, max, step, unit) {
        // Non-activatable PopupBaseMenuItems stay insensitive even after setSensitive(true).
        const item = new PopupMenu.PopupBaseMenuItem();
        const label = new St.Label({text: title, y_align: Clutter.ActorAlign.CENTER});
        const slider = new Slider(0);
        slider.x_expand = true;
        slider.accessible_name = title;
        const valueLabel = new St.Label({
            text: 'Unknown', style_class: 'mv7-value-label', y_align: Clutter.ActorAlign.CENTER,
        });
        item.add_child(label);
        item.add_child(slider);
        item.add_child(valueLabel);
        const value = () => Math.min(max, Math.max(min,
            Math.round((min + slider.value * (max - min)) / step) * step));
        const binding = {
            item, slider, field, action, dragging: false,
            valid: number => Number.isFinite(number) && number >= min && number <= max,
            apply: number => {
                slider.value = (number - min) / (max - min);
                valueLabel.text = `${number}${unit}`;
            },
        };
        slider.connect('drag-begin', () => { binding.dragging = true; });
        slider.connect('drag-end', () => {
            binding.dragging = false;
            if (!this._applying)
                this._send(binding, value());
        });
        slider.connect('notify::value', () => {
            if (this._applying || !this._enabled(binding))
                return;
            valueLabel.text = `${value()}${unit}`;
            if (!binding.dragging)
                this._debounce(binding, value());
        });
        menu.addMenuItem(item);
        this._bindings.push(binding);
    }

    _enabled(binding) {
        const state = this._snapshot?.state;
        return this._snapshot?.connection === 'connected' &&
            typeof state?.muted === 'boolean' && binding.valid(state[binding.field]) &&
            (binding.field !== 'gain_db' || (state.auto_level === false && state.gain_locked === false));
    }

    _debounce(binding, value) {
        this._cancelPending(binding.field);
        this._pending.set(binding.field, GLib.timeout_add(GLib.PRIORITY_DEFAULT, 200, () => {
            this._pending.delete(binding.field);
            this._send(binding, value);
            return GLib.SOURCE_REMOVE;
        }));
    }

    _cancelPending(field) {
        const source = this._pending.get(field);
        if (source)
            GLib.Source.remove(source);
        this._pending.delete(field);
    }

    _send(binding, value) {
        this._cancelPending(binding.field);
        if (!this._enabled(binding))
            return;
        if (!this._client?.send(binding.action, {value})) {
            this._showError('Could not send the change. Check the daemon connection.');
            this._update(this._snapshot);
        }
    }

    _stopClient() {
        this._generation++;
        for (const source of this._pending.values())
            GLib.Source.remove(source);
        this._pending.clear();
        this._client?.stop();
        this._client = null;
    }

    _connect() {
        this._stopClient();
        const generation = this._generation;
        this._update({connection: 'connecting', state: null, error: null});
        this._client = new DaemonClient({
            url: this._settings.get_string('daemon-url'),
            meterEnabled: this._settings.get_boolean('meter-enabled'),
            onMeter: reading => {
                if (this._button && generation === this._generation)
                    this._updateMeter(reading);
            },
            onUpdate: snapshot => {
                if (this._button && generation === this._generation)
                    this._update(snapshot);
            },
            onError: message => {
                if (this._button && generation === this._generation)
                    this._showError(message);
            },
        });
        this._client.start();
    }

    _update(snapshot) {
        this._snapshot = snapshot;
        const state = snapshot.connection === 'connected' ? snapshot.state : null;
        const ready = typeof state?.muted === 'boolean';
        let label, icon, style;
        if (ready) {
            label = state.muted ? 'MV7+ microphone muted' : 'MV7+ microphone live';
            icon = state.muted ? 'microphone-sensitivity-muted-symbolic' : 'audio-input-microphone-symbolic';
            style = state.muted ? 'mv7-muted' : '';
        } else {
            label = snapshot.connection === 'connecting' ? 'MV7+ connecting' :
                snapshot.connection === 'error' ? 'MV7+ state unknown' : 'MV7+ disconnected';
            icon = snapshot.connection === 'error' ? 'dialog-warning-symbolic' : 'network-offline-symbolic';
            style = snapshot.connection === 'error' ? 'mv7-error' : 'mv7-disconnected';
            for (const field of this._pending.keys())
                this._cancelPending(field);
        }
        this._button.accessible_name = label;
        this._icon.icon_name = icon;
        this._icon.style_class = `system-status-icon ${style}`;
        this._status.label.text = label;
        this._updateMeter(ready ? this._meterReading : null);
        this._error.visible = Boolean(snapshot.error);
        this._error.label.text = snapshot.error?.slice(0, 180) ?? '';
        this._applying = true;
        try {
            for (const binding of this._bindings) {
                const enabled = this._enabled(binding);
                binding.item.setSensitive(enabled);
                if (binding.slider) {
                    binding.slider.reactive = enabled;
                    binding.slider.can_focus = enabled;
                }

                if (ready && binding.valid(state[binding.field]) &&
                    !binding.dragging && !this._pending.has(binding.field))
                    binding.apply(state[binding.field]);
            }
        } finally {
            this._applying = false;
        }
    }

    _updateMeter(reading) {
        const enabled = this._settings.get_boolean('meter-enabled');
        this._meterReading = reading;
        const display = meterDisplay(enabled ? reading :
            {available: false, error: 'Live input meter disabled'});
        this._levelLabel.visible = enabled && this._settings.get_boolean('show-level-numbers');
        this._levelLabel.text = display.text;
        this._levelLabel.style_class = `mv7-panel-level${display.clipping ? ' mv7-muted' : ''}`;
        this._fillFraction = display.fraction;
        this._fillIcon.style_class = display.clipping ? 'mv7-muted' : 'mv7-input-fill';
        this._updateIconFill();
        this._meterTitle.text = `Input peak: ${display.text}`;
        this._meterBar.value = display.fraction;
        this._meterDetail.label.text = display.detail.slice(0, 180);
        this._meterBar.accessible_name = `MV7+ input level: ${display.detail}`;
        this._button.accessible_name = `${this._status.label.text}${enabled ? `, input ${display.text}` : ''}`;
    }

    _updateIconFill() {
        if (!this._settings || !this._fillIcon)
            return;
        const visible = this._settings.get_boolean('meter-enabled') &&
            this._meterReading?.available === true &&
            this._snapshot?.connection === 'connected' && this._snapshot.state?.muted === false;
        // Both participate in layout so themed icon padding stays reserved.
        this._fillIcon.opacity = visible ? 255 : 0;
        this._icon.opacity = visible ? 0 : 255;
        if (visible)
            this._fillIcon.queue_repaint();
    }

    _drawInputIcon() {
        const cr = this._fillIcon.get_context();
        const [width, height] = this._fillIcon.get_surface_size();
        const color = this._fillIcon.get_theme_node().get_foreground_color();
        const outline = this._button.get_theme_node().get_foreground_color();
        const scale = Math.min(width / 16, height / 20);
        cr.translate((width - 16 * scale) / 2, (height - 20 * scale) / 2);
        cr.scale(scale, scale);
        cr.setLineWidth(1.5);
        cr.arc(8, 5, 3, Math.PI, 0);
        cr.lineTo(11, 10);
        cr.arc(8, 10, 3, 0, Math.PI);
        cr.closePath();
        cr.save();
        cr.clip();
        cr.setSourceRGBA(0.45, 0.45, 0.45, 0.4);
        cr.paint();
        cr.setSourceRGBA(color.red / 255, color.green / 255, color.blue / 255, 1);
        const filled = 11 * (this._fillFraction ?? 0);
        cr.rectangle(5, 13 - filled, 6, filled);
        cr.fill();
        cr.restore();
        cr.setSourceRGBA(outline.red / 255, outline.green / 255, outline.blue / 255, 1);
        cr.newPath();
        cr.arc(8, 5, 3, Math.PI, 0);
        cr.lineTo(11, 10);
        cr.arc(8, 10, 3, 0, Math.PI);
        cr.closePath();
        cr.stroke();
        cr.arc(8, 9, 5, 0, Math.PI);
        cr.moveTo(8, 14);
        cr.lineTo(8, 18);
        cr.moveTo(5, 18);
        cr.lineTo(11, 18);
        cr.stroke();
        cr.$dispose();
    }

    _showError(message) {
        this._error.label.text = message.slice(0, 180);
        this._error.visible = true;
        console.error(`MV7+ Control: ${message}`);
    }

    _openApp() {
        const app = Gio.DesktopAppInfo.new('io.github.awlx.MV7.desktop');
        if (!app) {
            this._showError('Install the MV7+ Control native app, then log out and back in.');
            return;
        }
        try {
            app.launch([], global.create_app_launch_context(0, -1));
        } catch (error) {
            this._showError(error.message);
        }
    }

    _openWeb() {
        const endpoint = parseEndpoint(this._settings.get_string('daemon-url'));
        if (!endpoint.valid) {
            this._showError(endpoint.error);
            return;
        }
        Gio.AppInfo.launch_default_for_uri_async(endpoint.httpUrl,
            global.create_app_launch_context(0, -1), null, (_source, result) => {
                try {
                    Gio.AppInfo.launch_default_for_uri_finish(result);
                } catch (error) {
                    if (this._button)
                        this._showError(error.message);
                }
            });
    }

    disable() {
        this._stopClient();
        if (this._settingsChanged)
            this._settings.disconnect(this._settingsChanged);
        if (this._meterSettingsChanged)
            this._settings.disconnect(this._meterSettingsChanged);
        if (this._numberSettingsChanged)
            this._settings.disconnect(this._numberSettingsChanged);
        this._settingsChanged = 0;
        this._meterSettingsChanged = 0;
        this._numberSettingsChanged = 0;
        this._settings = null;
        this._button?.destroy();
        this._button = null;
        this._icon = null;
        this._fillIcon = null;
        this._status = null;
        this._error = null;
        this._bindings = [];
    }
}
