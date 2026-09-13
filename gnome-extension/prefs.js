import Adw from 'gi://Adw';
import Gio from 'gi://Gio';
import {ExtensionPreferences} from 'resource:///org/gnome/Shell/Extensions/js/extensions/prefs.js';
import {parseEndpoint} from './state.js';

export default class MV7Preferences extends ExtensionPreferences {
    fillPreferencesWindow(window) {
        const settings = this.getSettings();
        const page = new Adw.PreferencesPage({title: 'MV7+ Control'});
        const group = new Adw.PreferencesGroup({
            title: 'Go daemon',
            description: 'Start mv7web separately. The extension does not open HID devices ' +
                'or change the system audio mute. Use an SSH tunnel for remote microphones.',
        });
        const endpoint = new Adw.EntryRow({
            title: 'Daemon URL', text: settings.get_string('daemon-url'),
            show_apply_button: true,
        });
        const feedback = new Adw.ActionRow({
            title: 'Local connection', subtitle: 'Default: http://127.0.0.1:8090',
            use_markup: false, subtitle_lines: 0,
        });
        endpoint.connect('apply', () => {
            const result = parseEndpoint(endpoint.text);
            if (!result.valid) {
                endpoint.add_css_class('error');
                feedback.subtitle = result.error;
                return;
            }
            endpoint.remove_css_class('error');
            settings.set_string('daemon-url', result.httpUrl.replace(/\/$/, ''));
            feedback.subtitle = 'Saved. The extension will reconnect to this daemon.';
        });
        group.add(endpoint);
        group.add(feedback);
        const meter = new Adw.SwitchRow({
            title: 'Live input meter',
            subtitle: 'Show input dBFS in the panel and menu. Shares MV7+ audio; nothing is recorded.',
        });
        settings.bind('meter-enabled', meter, 'active', Gio.SettingsBindFlags.DEFAULT);
        group.add(meter);
        const numbers = new Adw.SwitchRow({
            title: 'Show level numbers in panel',
            subtitle: 'Display numeric dBFS beside the filling microphone icon.',
        });
        settings.bind('show-level-numbers', numbers, 'active', Gio.SettingsBindFlags.DEFAULT);
        group.add(numbers);
        page.add(group);
        window.add(page);
    }
}
