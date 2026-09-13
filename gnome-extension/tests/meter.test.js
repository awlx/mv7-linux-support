import test from 'node:test';
import assert from 'node:assert/strict';
import {DaemonClient} from '../daemonClient.js';
import {parseMeter, meterDisplay, buildCommand} from '../state.js';
import {FakeClock, makeFakeTransport} from './helpers.js';

const level = {available: true, peak_dbfs: -6.02, rms_dbfs: -9.03, clipping: false, source: 'MV7+'};
function setup(opts = {}) {
    const clock = new FakeClock();
    const transportFactory = makeFakeTransport();
    const readings = [];
    const client = new DaemonClient({clock, transportFactory,
        backoff: {base: 50, cap: 50, jitter: 0},
        onMeter: value => readings.push(value), ...opts});
    client.start();
    const socket = transportFactory.last();
    socket.emitOpen();
    const ready = (supported = true) => {
        socket.emitMessage({type: 'status', connected: true, meter_supported: supported});
        socket.emitMessage({type: 'state', state: {muted: false, gain_db: 12.5}});
    };
    return {clock, client, socket, readings, ready, transportFactory};
}

test('meter readings strictly distinguish signal, silence, clipping and missing data', () => {
    assert.equal(meterDisplay(level).text, '-6 dBFS');
    assert.equal(meterDisplay(level).fraction, (-6.02 + 60) / 60);
    const silence = {...level, peak_dbfs: -90, rms_dbfs: -90};
    assert.equal(meterDisplay(silence).text, '<= -90 dBFS');
    assert.equal(meterDisplay(silence).fraction, 0);
    assert.equal(meterDisplay(null).text, '-- dBFS');
    assert.match(meterDisplay({...level, peak_dbfs: 0, clipping: true}).detail, /CLIPPING/);
    for (const patch of [{peak_dbfs: 1}, {peak_dbfs: -91}, {rms_dbfs: NaN},
        {peak_dbfs: '-6'}, {clipping: 1}, {rms_dbfs: -3}, {rms_dbfs: Infinity}])
        assert.equal(parseMeter({...level, ...patch}).available, false);
    assert.equal(buildCommand('subscribe_meter', {enabled: 'true'}).valid, false);
    assert.equal(buildCommand('subscribe_meter', {enabled: false}).valid, true);
});

test('meter subscriptions require capability and a consumer, and are sent once', () => {
    const s = setup();
    assert.equal(s.socket.sent.length, 0);
    s.ready(false);
    assert.equal(s.socket.sent.length, 0);
    assert.match(s.readings.at(-1).error, /Update mv7web/);
    s.ready();
    s.ready();
    assert.deepEqual(s.socket.sent.map(JSON.parse),
        [{action: 'subscribe_meter', params: {enabled: true}}]);
    s.client.stop();
    const noConsumer = setup({onMeter: null});
    noConsumer.ready();
    assert.equal(noConsumer.socket.sent.length, 0);
    noConsumer.client.stop();
});

test('meter expires independently of hardware state, and bad levels never clear mute', () => {
    const s = setup();
    s.ready();
    s.socket.emitMessage({type: 'meter', meter: level});
    assert.equal(s.readings.at(-1).available, true);
    s.clock.tick(1999);
    assert.equal(s.readings.at(-1).available, true);
    s.clock.tick(1);
    assert.equal(s.readings.at(-1).available, false);
    assert.equal(s.client.connection, 'connected');
    s.socket.emitMessage({type: 'meter', meter: {available: true}});
    assert.match(s.readings.at(-1).error, /Invalid/);
    assert.equal(s.client.connection, 'connected');
    s.client.stop();
    assert.equal(s.clock.pending(), 0);
});

test('continuous meter traffic cannot keep stale hardware mute alive', () => {
    const s = setup();
    s.ready();
    for (let i = 0; i < 15; i++) {
        s.socket.emitMessage({type: 'meter', meter: level});
        s.clock.tick(1000);
    }
    assert.notEqual(s.client.connection, 'connected');
    assert.equal(s.readings.at(-1).available, false);
    s.client.stop();
});

test('disable, disconnect and stop clear readings and reject late frames', () => {
    const s = setup();
    s.ready();
    s.socket.emitMessage({type: 'meter', meter: level});
    s.client.setMeterEnabled(false);
    assert.deepEqual(JSON.parse(s.socket.sent.at(-1)),
        {action: 'subscribe_meter', params: {enabled: false}});
    s.socket.emitMessage({type: 'meter', meter: level});
    assert.equal(s.readings.at(-1).available, false);
    s.client.setMeterEnabled(true);
    s.socket.emitMessage({type: 'meter', meter: level});
    assert.equal(s.readings.at(-1).available, true);
    s.socket.emitMessage({type: 'status', connected: false, meter_supported: true});
    s.socket.emitMessage({type: 'meter', meter: level});
    assert.equal(s.readings.at(-1).available, false);
    s.client.stop();
    s.socket.emitMessage({type: 'meter', meter: level});
    assert.equal(s.readings.at(-1).available, false);
    assert.equal(s.clock.pending(), 0);
});

test('reconnect negotiates a new subscription rather than reusing old samples', () => {
    const s = setup();
    s.ready();
    s.socket.emitMessage({type: 'meter', meter: level});
    s.socket.emitClose();
    s.clock.tick(50);
    const next = s.transportFactory.last();
    next.emitOpen();
    next.emitMessage({type: 'status', connected: true, meter_supported: true});
    assert.equal(next.sent.length, 1);
    assert.equal(s.readings.at(-1).available, false);
    s.client.stop();
});
