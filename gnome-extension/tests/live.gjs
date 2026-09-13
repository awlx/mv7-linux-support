import GLib from 'gi://GLib';
import Soup from 'gi://Soup?version=3.0';
import System from 'system';
import {DaemonClient} from '../daemonClient.js';

const loop = new GLib.MainLoop(null, false);
const server = new Soup.Server();
let client;
let connection;
let phase = 0;
let exitCode = 0;
let done = false;
let deadline;
let lastMeter = null;

function finish(error = null) {
    if (done)
        return;
    done = true;
    if (error) {
        printerr(error.message ?? error);
        exitCode = 1;
    } else {
        print('Live libsoup round-trip passed: state, subscribed meter, command, confirmed mute, disconnect.');
    }
    if (deadline)
        GLib.Source.remove(deadline);
    deadline = 0;
    client?.stop();
    if (connection?.get_state() === Soup.WebsocketState.OPEN)
        connection.close(Soup.WebsocketCloseCode.NORMAL, null);
    server.disconnect();
    loop.quit();
}

server.add_websocket_handler('/ws', null, null, (_server, _message, _path, socket) => {
    connection = socket;
    socket.connect('message', (_connection, _type, bytes) => {
        try {
            const command = JSON.parse(new TextDecoder().decode(bytes.get_data()));
            if (command.action === 'subscribe_meter' && command.params.enabled === true) {
                socket.send_text(JSON.stringify({type: 'meter', meter: {
                    available: true, peak_dbfs: -6.02, rms_dbfs: -9.03,
                    clipping: false, source: 'in-memory test fixture',
                }}));
                return;
            }
            if (command.action !== 'set_mute' || command.params.value !== true)
                throw new Error('Unexpected command received by fixture');
            socket.send_text(JSON.stringify({type: 'state',
                state: {muted: true, gain_db: 12.5, gain_locked: false, auto_level: false}}));
        } catch (error) {
            finish(error);
        }
    });
    socket.send_text(JSON.stringify({type: 'status', connected: true, meter_supported: true}));
    socket.send_text(JSON.stringify({type: 'state',
        state: {muted: false, gain_db: 12.5, gain_locked: false, auto_level: false}}));
});
server.listen_local(0, Soup.ServerListenOptions.IPV4_ONLY);
const uri = server.get_uris()[0];
client = new DaemonClient({
    url: `http://127.0.0.1:${uri.get_port()}`,
    onError: message => finish(new Error(message)),
    onMeter: reading => {
        lastMeter = reading;
        if (done || phase !== 1 || !reading.available)
            return;
        if (reading.peak_dbfs !== -6.02 || reading.rms_dbfs !== -9.03) {
            finish(new Error('Live client corrupted the measured levels'));
            return;
        }
        phase = 2;
        if (!client.send('set_mute', {value: true}))
            finish(new Error('Connected client did not send mute'));
    },
    onUpdate: snapshot => {
        if (done)
            return;
        try {
            if (phase === 0 && snapshot.connection === 'connected') {
                if (snapshot.state.gain_db !== 12.5 || snapshot.state.muted !== false)
                    throw new Error('Client must preserve the daemon state shape');
                phase = 1;
            } else if (phase === 2 && snapshot.state?.muted === true) {
                phase = 3;
                connection.send_text(JSON.stringify({type: 'status', connected: false, meter_supported: true}));
            } else if (phase === 3 && snapshot.connection === 'disconnected') {
                if (snapshot.state !== null || client.send('set_mute', {value: false}))
                    throw new Error('Unavailable hardware must clear state and block writes');
                if (lastMeter?.available !== false)
                    throw new Error('Disconnect must clear the live meter');
                finish();
            }
        } catch (error) {
            finish(error);
        }
    },
});
deadline = GLib.timeout_add(GLib.PRIORITY_DEFAULT, 8000, () => {
    deadline = 0;
    finish(new Error(`Round-trip timed out in phase ${phase}`));
    return GLib.SOURCE_REMOVE;
});
client.start();
loop.run();
System.exit(exitCode);
