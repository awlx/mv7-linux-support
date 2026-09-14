// Loaded as extension.js by shell-layout.sh, beside the staged implementation.
import GLib from 'gi://GLib';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import MV7Extension from './implementation.js';
import {demoState, run} from './tests/shell-layout.js';

class FixtureIndicator extends MV7Extension {
    _connect() {
        this._update({connection: 'connected', state: demoState});
    }
}

export default class LayoutFixture extends Extension {
    enable() {
        this._indicator = new FixtureIndicator(this.metadata);
        this._indicator.enable();
        this._start = GLib.timeout_add(GLib.PRIORITY_DEFAULT, 1500, () => {
            this._start = 0;
            run(this._indicator).then(result => {
                GLib.file_set_contents(GLib.getenv('MV7_LAYOUT_RESULT'), JSON.stringify(result, null, 2));
                global.context.terminate();
            }).catch(error => {
                console.error(error);
                GLib.file_set_contents(GLib.getenv('MV7_LAYOUT_RESULT'),
                    JSON.stringify({error: `${error.message}\n${error.stack}`}));
                global.context.terminate();
            });
            return GLib.SOURCE_REMOVE;
        });
    }

    disable() {
        if (this._start)
            GLib.Source.remove(this._start);
        this._start = 0;
        this._indicator?.disable();
        this._indicator = null;
    }
}
