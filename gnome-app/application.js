import Adw from 'gi://Adw?version=1';
import Cairo from 'cairo';
import Gdk from 'gi://Gdk?version=4.0';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import GObject from 'gi://GObject';
import Gtk from 'gi://Gtk?version=4.0';

import {DaemonClient} from '../gnome-extension/daemonClient.js';
import {advancePeakHold, meterDisplay, parseMeter} from '../gnome-extension/state.js';
import {APP_ID, CONTROLS, PAGES, commandFor, controlEnabled, validValue} from './controls.js';

const directory = Gio.File.new_for_uri(import.meta.url).get_parent().get_path();
const schemaSource = Gio.SettingsSchemaSource.new_from_directory(
    GLib.build_filenamev([directory, 'schemas']),
    Gio.SettingsSchemaSource.get_default(), false);
const schema = schemaSource.lookup(APP_ID, false);
if (!schema)
    throw new Error('MV7+ settings schema is missing. Run glib-compile-schemas gnome-app/schemas or reinstall the application.');

export const MV7Application = GObject.registerClass(
class MV7Application extends Adw.Application {
    _init() {
        super._init({application_id: APP_ID, flags: Gio.ApplicationFlags.DEFAULT_FLAGS});
        this._settings = new Gio.Settings({settings_schema: schema});
        this._bindings = [];
        this._pending = new Map();
        this._snapshot = {connection: 'disconnected', state: null, error: null};
        this._generation = 0;
        this._applying = false;
        this._client = null;
        this._window = null;
        this._lastError = null;
        this._meterReading = null;
        this._heldPeak = null;
        this._peakTimer = 0;
        this._meterStyle = null;
        this.connect('activate', () => this._activate());
        this.connect('shutdown', () => {
            this._stopClient();
            if (this._meterStyle) {
                Gtk.StyleContext.remove_provider_for_display(this._meterStyleDisplay, this._meterStyle);
                this._meterStyle = null;
            }
        });
    }

    _activate() {
        if (this._window) {
            this._window.present();
            return;
        }
        this._window = new Adw.ApplicationWindow({
            application: this, title: 'MV7+ Control',
            default_width: 780, default_height: 820,
        });
        this._overlay = new Adw.ToastOverlay();
        const layout = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL});
        this._stack = new Adw.ViewStack({vexpand: true});
        const header = new Adw.HeaderBar();
        header.set_title_widget(new Adw.ViewSwitcher({
            stack: this._stack, policy: Adw.ViewSwitcherPolicy.WIDE,
        }));
        layout.append(header);
        this._statusIcon = new Gtk.Image({
            icon_name: 'network-offline-symbolic', pixel_size: 24,
        });
        this._statusTitle = new Gtk.Label({
            label: 'Microphone state unknown', xalign: 0, hexpand: true,
            wrap: true, css_classes: ['heading'],
        });
        const status = new Gtk.Box({spacing: 12});
        status.append(this._statusIcon);
        status.append(this._statusTitle);
        const meter = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, spacing: 6});
        const meterHeader = new Gtk.Box({spacing: 12});
        this._meterTitle = new Gtk.Label({label: 'Input peak: -- dBFS', xalign: 0, hexpand: true});
        this._peakHoldLabel = new Gtk.Label({
            label: 'Peak hold: -- dBFS', xalign: 1,
            tooltip_text: 'Holds peaks for one second, then falls by 12 dB per second.',
        });
        meterHeader.append(this._meterTitle);
        meterHeader.append(this._peakHoldLabel);
        this._meterBar = new Gtk.LevelBar({min_value: 0, max_value: 1, value: 0});
        this._meterBar.add_css_class('mv7-input-meter');
        this._meterBar.tooltip_text = 'Green below -18 dBFS, yellow near -6 dBFS, red near full scale. ' +
            'CLIPPING indicates actual full-scale samples.';
        this._meterStyle = new Gtk.CssProvider();
        this._meterStyle.load_from_data(`
            .mv7-input-meter trough {
                padding: 0; min-height: 12px; border-radius: 4px;
                background-color: alpha(currentColor, 0.12);
            }
            .mv7-input-meter block {
                min-height: 12px; min-width: 0; margin: 0; padding: 0;
                border: none; background: transparent; box-shadow: none;
            }
        `, -1);
        this._meterStyleDisplay = this._window.get_display();
        Gtk.StyleContext.add_provider_for_display(this._meterStyleDisplay,
            this._meterStyle, Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION);
        const meterOverlay = new Gtk.Overlay();
        meterOverlay.set_child(this._meterBar);
        this._meterDrawing = new Gtk.DrawingArea({can_target: false});
        this._meterDrawing.set_draw_func((_area, cr, width, height) => this._drawMeter(cr, width, height));
        meterOverlay.add_overlay(this._meterDrawing);
        this._meterDetail = new Gtk.Label({
            label: 'Waiting for input level', xalign: 0, wrap: true, css_classes: ['dim-label'],
        });
        meter.append(meterHeader);
        meter.append(meterOverlay);
        meter.append(this._meterDetail);
        const liveContent = new Gtk.Box({
            orientation: Gtk.Orientation.VERTICAL, spacing: 12,
            margin_start: 12, margin_end: 12, margin_top: 12, margin_bottom: 12,
        });
        liveContent.append(status);
        liveContent.append(meter);
        this._liveRow = new Adw.PreferencesRow({
            title: 'Microphone status and input level',
            child: liveContent, activatable: false, selectable: false,
        });
        this._liveGroup = new Adw.PreferencesGroup({title: 'Microphone'});
        this._livePage = new Adw.PreferencesPage({vexpand: false});
        this._livePage.add(this._liveGroup);
        layout.append(this._livePage);
        layout.append(this._stack);
        this._overlay.set_child(layout);
        this._window.set_content(this._overlay);

        const pages = new Map();
        const groups = new Map();
        for (const page of PAGES) {
            const widget = new Adw.PreferencesPage();
            pages.set(page.id, widget);
            this._stack.add_titled_with_icon(widget, page.id, page.title, page.icon);
        }
        for (const control of CONTROLS) {
            if (control.field === 'muted') {
                this._addControl(this._liveGroup, control);
                continue;
            }
            const key = `${control.page}/${control.group}`;
            if (!groups.has(key)) {
                const group = new Adw.PreferencesGroup({title: control.group});
                pages.get(control.page).add(group);
                groups.set(key, group);
            }
            this._addControl(groups.get(key), control);
        }
        this._liveGroup.add(this._liveRow);
        this._buildConnectionPage();
        this._applySnapshot(this._snapshot);
        this._window.present();
        this._connectClient();
    }

    _addControl(group, control) {
        const row = new Adw.ActionRow({
            title: control.title, subtitle: control.subtitle ?? '', use_markup: false,
        });
        const binding = {control, row, widgets: [], editing: false};
        const changed = value => {
            if (!this._applying)
                this._queueCommand(binding, value);
        };
        if (control.kind === 'switch') {
            const widget = new Gtk.Switch({valign: Gtk.Align.CENTER});
            widget.connect('notify::active', () => changed(widget.active));
            binding.setValue = value => { widget.active = value; };
            binding.widgets.push(widget);
            row.set_activatable_widget(widget);
        } else if (control.kind === 'choice') {
            const widget = Gtk.DropDown.new_from_strings(control.options);
            widget.valign = Gtk.Align.CENTER;
            widget.connect('notify::selected', () => changed(widget.selected + (control.offset ?? 0)));
            binding.setValue = value => { widget.selected = value - (control.offset ?? 0); };
            binding.widgets.push(widget);
            row.set_activatable_widget(widget);
        } else if (control.kind === 'range') {
            const adjustment = new Gtk.Adjustment({
                lower: control.min, upper: control.max,
                step_increment: control.step, page_increment: control.step * 6,
            });
            const scale = new Gtk.Scale({
                orientation: Gtk.Orientation.HORIZONTAL, adjustment,
                draw_value: false, width_request: 140, valign: Gtk.Align.CENTER,
            });
            const spin = new Gtk.SpinButton({
                adjustment, digits: control.step < 1 ? 1 : 0,
                numeric: true, valign: Gtk.Align.CENTER, width_chars: 5,
            });
            const focus = new Gtk.EventControllerFocus();
            focus.connect('enter', () => { binding.editing = true; });
            focus.connect('leave', () => { binding.editing = false; });
            spin.add_controller(focus);
            adjustment.connect('value-changed', () => changed(adjustment.value));
            binding.setValue = value => { adjustment.value = value; };
            binding.widgets.push(scale, spin);
        } else if (control.kind === 'color') {
            const widget = new Gtk.ColorDialogButton({
                dialog: new Gtk.ColorDialog({title: control.title, with_alpha: false}),
                valign: Gtk.Align.CENTER,
            });
            widget.connect('notify::rgba', () => {
                const rgba = widget.get_rgba();
                changed([rgba.red, rgba.green, rgba.blue].map(value => Math.round(value * 255)));
            });
            binding.setValue = value => {
                const rgba = new Gdk.RGBA();
                [rgba.red, rgba.green, rgba.blue] = value.map(channel => channel / 255);
                rgba.alpha = 1;
                widget.set_rgba(rgba);
            };
            binding.widgets.push(widget);
        }
        for (const widget of binding.widgets) {
            widget.update_property([Gtk.AccessibleProperty.LABEL], [control.title]);
            row.add_suffix(widget);
        }
        group.add(row);
        this._bindings.push(binding);
    }

    _buildConnectionPage() {
        const page = new Adw.PreferencesPage();
        this._stack.add_titled_with_icon(page, 'connection', 'Connection', 'network-server-symbolic');
        const connection = new Adw.PreferencesGroup({
            title: 'Go daemon',
            description: 'Start mv7web separately, or enable its user service. ' +
                'This application controls microphone hardware, not the system audio mute.',
        });
        this._endpoint = new Adw.EntryRow({
            title: 'Daemon URL', text: this._settings.get_string('daemon-url'),
            show_apply_button: true,
        });
        this._endpoint.connect('apply', () => {
            this._settings.set_string('daemon-url', this._endpoint.text.trim());
            this._connectClient();
        });
        connection.add(this._endpoint);
        this._connectionRow = new Adw.ActionRow({
            title: 'Connection status', subtitle: 'Waiting for the daemon.', use_markup: false,
            subtitle_lines: 0,
        });
        const reconnect = new Gtk.Button({
            label: 'Reconnect', valign: Gtk.Align.CENTER,
        });
        reconnect.connect('clicked', () => this._connectClient());
        this._connectionRow.add_suffix(reconnect);
        connection.add(this._connectionRow);
        const meterToggle = new Adw.SwitchRow({
            title: 'Live input meter',
            subtitle: 'Shares the MV7+ audio input; no audio is saved or sent to this app.',
        });
        this._settings.bind('meter-enabled', meterToggle, 'active', Gio.SettingsBindFlags.DEFAULT);
        meterToggle.connect('notify::active', () => {
            this._client?.setMeterEnabled(meterToggle.active);
            this._applyMeter(null);
        });
        connection.add(meterToggle);
        page.add(connection);

        const device = new Adw.PreferencesGroup({title: 'Microphone'});
        this._identity = new Adw.ActionRow({
            title: 'Not connected', subtitle: 'Firmware and serial number are unavailable.',
            use_markup: false, subtitle_lines: 0,
        });
        device.add(this._identity);
        page.add(device);

        const danger = new Adw.PreferencesGroup({
            title: 'Reset', description: 'Factory reset erases the microphone settings.',
        });
        const row = new Adw.ActionRow({title: 'Factory defaults'});
        this._reset = new Gtk.Button({
            label: 'Reset microphone', valign: Gtk.Align.CENTER,
            css_classes: ['destructive-action'],
        });
        this._reset.connect('clicked', () => this._confirmReset());
        row.add_suffix(this._reset);
        danger.add(row);
        page.add(danger);
        const about = new Adw.PreferencesGroup({
            description: 'Unofficial community software. Not affiliated with or supported by Shure. ' +
                'Shure and MV7+ are trademarks of Shure Incorporated.',
        });
        page.add(about);
    }

    _stopClient() {
        this._generation++;
        this._stopPeakAnimation();
        this._heldPeak = null;
        this._meterReading = null;
        for (const source of this._pending.values())
            GLib.Source.remove(source);
        this._pending.clear();
        this._client?.stop();
        this._client = null;
    }

    _connectClient() {
        this._stopClient();
        this._lastError = null;
        const generation = this._generation;
        this._applySnapshot({connection: 'connecting', state: null, error: null});
        try {
            this._client = new DaemonClient({
                url: this._settings.get_string('daemon-url'),
                allowFactoryReset: true,
                meterEnabled: this._settings.get_boolean('meter-enabled'),
                onMeter: reading => {
                    if (generation === this._generation)
                        this._applyMeter(reading);
                },
                onUpdate: snapshot => {
                    if (generation === this._generation)
                        this._applySnapshot(snapshot);
                },
                onError: message => {
                    if (generation === this._generation)
                        this._showError(message);
                },
            });
            this._client.start();
        } catch (error) {
            this._applySnapshot({connection: 'error', state: null, error: error.message});
            this._showError(error.message);
        }
    }

    _applySnapshot(snapshot) {
        this._snapshot = snapshot;
        const ready = snapshot.connection === 'connected' &&
            typeof snapshot.state?.muted === 'boolean';
        const state = ready ? snapshot.state : null;
        if (!ready) {
            this._applyMeter(null);
            for (const source of this._pending.values())
                GLib.Source.remove(source);
            this._pending.clear();
        } else {
            this._lastError = null;
        }

        const label = ready
            ? (state.muted ? 'Microphone muted' : 'Microphone live')
            : ({connecting: 'Connecting to microphone', error: 'Microphone state unknown',
                disconnected: 'Microphone unavailable'}[snapshot.connection] ?? 'Microphone state unknown');
        this._statusTitle.label = label;
        this._statusIcon.icon_name = ready
            ? (state.muted ? 'microphone-sensitivity-muted-symbolic' : 'audio-input-microphone-symbolic')
            : (snapshot.connection === 'error' ? 'dialog-warning-symbolic' : 'network-offline-symbolic');
        this._statusIcon.update_property([Gtk.AccessibleProperty.LABEL], [label]);
        this._window.title = `${label} - MV7+ Control`;
        this._connectionRow.subtitle = snapshot.error || label;
        this._identity.title = state?.device_name || 'Not connected';
        this._identity.subtitle = state
            ? `Firmware: ${state.firmware || 'Not reported'}\nSerial: ${state.serial || 'Not reported'}`
            : 'Firmware and serial number are unavailable.';
        this._reset.sensitive = ready;
        this._applying = true;
        try {
            for (const binding of this._bindings) {
                const {control, row} = binding;
                row.sensitive = controlEnabled(control, snapshot);
                row.subtitle = ready && !validValue(control, state[control.field])
                    ? 'Not reported by the daemon.' : control.subtitle ?? '';
                if (control.field === 'gain_db' && ready && !row.sensitive)
                    row.subtitle = 'Disable Auto Level and unlock gain to adjust manually.';
                if (ready && validValue(control, state[control.field]) &&
                    !this._pending.has(control.field) && !binding.editing)
                    binding.setValue(state[control.field]);
            }
        } finally {
            this._applying = false;
        }
    }

    _applyMeter(reading) {
        this._meterReading = parseMeter(this._settings.get_boolean('meter-enabled') ? reading :
            {available: false, error: 'Live input meter disabled'});
        const display = meterDisplay(this._meterReading);
        this._meterTitle.label = `Input peak: ${display.text}`;
        this._meterDetail.label = display.detail;
        this._meterBar.value = display.fraction;
        this._meterBar.update_property([Gtk.AccessibleProperty.LABEL], [`Input level: ${display.detail}`]);
        if (display.clipping)
            this._meterTitle.add_css_class('error');
        else
            this._meterTitle.remove_css_class('error');
        if (this._updatePeakHold()) {
            if (!this._peakTimer) {
                this._peakTimer = GLib.timeout_add(GLib.PRIORITY_DEFAULT, 50, () => {
                    if (this._updatePeakHold())
                        return GLib.SOURCE_CONTINUE;
                    this._peakTimer = 0;
                    return GLib.SOURCE_REMOVE;
                });
            }
        } else {
            this._stopPeakAnimation();
        }
    }

    _updatePeakHold() {
        this._heldPeak = advancePeakHold(this._heldPeak, this._meterReading,
            GLib.get_monotonic_time() / 1000);
        const display = meterDisplay(this._heldPeak);
        this._peakHoldLabel.label = `Peak hold: ${display.text}`;
        if (display.clipping)
            this._peakHoldLabel.add_css_class('error');
        else
            this._peakHoldLabel.remove_css_class('error');
        this._meterDrawing.queue_draw();
        return this._heldPeak.available && this._heldPeak.peak_dbfs > this._meterReading.peak_dbfs;
    }

    _stopPeakAnimation() {
        if (this._peakTimer) {
            GLib.Source.remove(this._peakTimer);
            this._peakTimer = 0;
        }
    }

    _drawMeter(cr, width, height) {
        if (width <= 0 || height <= 0)
            return;
        const rtl = this._meterBar.get_direction() === Gtk.TextDirection.RTL;
        const radius = Math.min(4, width / 2, height / 2);
        cr.save();
        cr.newSubPath();
        cr.arc(width - radius, radius, radius, -Math.PI / 2, 0);
        cr.arc(width - radius, height - radius, radius, 0, Math.PI / 2);
        cr.arc(radius, height - radius, radius, Math.PI / 2, Math.PI);
        cr.arc(radius, radius, radius, Math.PI, 3 * Math.PI / 2);
        cr.closePath();
        cr.clip();

        // Anchor the gradient to the full scale, not the changing filled width.
        const gradient = new Cairo.LinearGradient(rtl ? width : 0, 0, rtl ? 0 : width, 0);
        for (const [offset, red, green, blue] of [
            [0, 51, 209, 122], [0.7, 51, 209, 122],
            [0.9, 246, 211, 45], [1, 237, 51, 59],
        ])
            gradient.addColorStopRGB(offset, red / 255, green / 255, blue / 255);
        const filledWidth = this._meterBar.value * width;
        cr.setSource(gradient);
        cr.rectangle(rtl ? width - filledWidth : 0, 0, filledWidth, height);
        cr.fill();

        if (this._heldPeak?.available) {
            const fraction = meterDisplay(this._heldPeak).fraction;
            const x = Math.max(0, Math.min(width - 2, (rtl ? 1 - fraction : fraction) * width - 1));
            const color = this._peakHoldLabel.get_style_context().get_color();
            cr.setSourceRGBA(color.red, color.green, color.blue, color.alpha);
            cr.rectangle(x, 0, 2, height);
            cr.fill();
        }
        cr.restore();
    }

    _queueCommand(binding, value) {
        if (!controlEnabled(binding.control, this._snapshot))
            return;
        let command;
        try {
            command = commandFor(binding.control, value);
        } catch (error) {
            this._showError(error.message);
            return;
        }
        const key = binding.control.field;
        if (this._pending.has(key))
            GLib.Source.remove(this._pending.get(key));
        this._pending.set(key, GLib.timeout_add(GLib.PRIORITY_DEFAULT, 250, () => {
            this._pending.delete(key);
            if (controlEnabled(binding.control, this._snapshot) &&
                !this._client?.send(command.action, command.params)) {
                this._showError('Could not send the change. Check the daemon connection.');
                this._applySnapshot(this._snapshot);
            }
            return GLib.SOURCE_REMOVE;
        }));
    }

    _confirmReset() {
        if (this._snapshot.connection !== 'connected')
            return;
        const generation = this._generation;
        const state = this._snapshot.state;
        const dialog = new Adw.MessageDialog({
            application: this, transient_for: this._window,
            modal: true, destroy_with_parent: true,
            heading: 'Reset microphone settings?',
            body: 'All MV7+ settings will return to their factory defaults. ' +
                'The microphone may disconnect briefly. This cannot be undone.',
        });
        dialog.add_response('cancel', 'Cancel');
        dialog.add_response('reset', 'Reset microphone');
        dialog.set_response_appearance('reset', Adw.ResponseAppearance.DESTRUCTIVE);
        dialog.set_default_response('cancel');
        dialog.set_close_response('cancel');
        dialog.connect('response', (_dialog, response) => {
            if (response !== 'reset')
                return;
            if (generation !== this._generation ||
                this._snapshot.connection !== 'connected' ||
                state.serial !== this._snapshot.state?.serial) {
                this._showError('The microphone connection changed. Reopen the reset dialog to try again.');
                return;
            }
            if (this._client?.send('factory_reset', {}))
                this._overlay.add_toast(new Adw.Toast({title: 'Reset requested. Waiting for the microphone.'}));
            else
                this._showError('Could not request a reset. Check the daemon connection.');
        });
        dialog.present();
    }

    _showError(message) {
        this._connectionRow.subtitle = message;
        if (message !== this._lastError) {
            this._overlay.add_toast(new Adw.Toast({title: message, timeout: 6}));
            this._lastError = message;
        }
    }
});
