// SPDX-License-Identifier: GPL-3.0-only
//
// Lifecycle tests for DaemonClient, driven entirely through the injectable
// transport + virtual clock harness (no GJS required).
//   node --test

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { DaemonClient } from '../daemonClient.js';
import { FakeClock, makeFakeTransport, makeRecorder } from './helpers.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const fixture = (name) => JSON.parse(readFileSync(join(HERE, 'fixtures', name), 'utf8'));
const MUTED = fixture('state-muted.json');
const LIVE = fixture('state-live.json');

function makeClient(overrides = {}) {
    const clock = new FakeClock();
    const transport = makeFakeTransport();
    const rec = makeRecorder();
    const client = new DaemonClient({
        url: 'http://127.0.0.1:8090',
        onUpdate: rec.onUpdate,
        onError: rec.onError,
        watchdogMs: 1000,
        backoff: { base: 500, factor: 2, cap: 5000, jitter: 0 },
        clock,
        transportFactory: transport,
        ...overrides,
    });
    return { clock, transport, rec, client };
}

test('happy path: connecting -> connected only after a valid state arrives', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    assert.equal(rec.lastUpdate().connection, 'connecting');

    const t = transport.last();
    // The WS URL derived from the endpoint is the loopback /ws.
    assert.equal(t.opts.wsUrl, 'ws://127.0.0.1:8090/ws');

    t.emitOpen();
    // Socket open alone must NOT report connected.
    assert.equal(client.connection, 'connecting');

    t.emitMessage(MUTED);
    assert.equal(client.connection, 'connected');
    const last = rec.lastUpdate();
    assert.equal(last.connection, 'connected');
    assert.equal(last.state.muted, true);
    assert.equal(last.state.gain_db, 24.5);
    assert.deepEqual(last.state, MUTED.state);
    // observed connection sequence
    assert.deepEqual(rec.connections(), ['connecting', 'connected']);
    client.stop();
});

test('malformed / missing mute never surfaces as false while connected', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    t.emitMessage({ type: 'state', state: { muted: 'weird', gain_db: 5 } });
    assert.equal(client.connection, 'error');
    assert.equal(rec.lastUpdate().state, null);

    t.emitMessage({ type: 'state', state: { gain_db: 6 } }); // mute absent
    assert.equal(rec.lastUpdate().state, null);
    client.stop();
});

test('invalid JSON clears stale state while unknown events are ignored', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    t.emitMessage(LIVE);
    const before = rec.updates.length;
    t.emitMessage('{ this is not json');
    t.emitMessage(JSON.stringify({ type: 'totally_unknown' }));
    assert.equal(client.connection, 'error');
    assert.equal(rec.lastUpdate().state, null);
    assert.equal(rec.updates.length, before + 1);
    client.stop();
});

test('status:false clears state; status:true before state does not flicker', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();

    // New-server ordering: status:true precedes the first state.
    t.emitMessage({ type: 'status', connected: true });
    assert.equal(client.connection, 'connecting'); // still no state
    t.emitMessage(LIVE);
    assert.equal(client.connection, 'connected');

    const healthy = rec.lastUpdate().state;
    const n = rec.updates.length;
    // status:true again (emitted before each state) must not clear/flip.
    t.emitMessage({ type: 'status', connected: true });
    assert.equal(client.connection, 'connected');
    assert.equal(rec.updates.length, n); // no flicker update
    assert.equal(rec.lastUpdate().state, healthy);

    // device lost:
    t.emitMessage({ type: 'status', connected: false, error: 'device unplugged' });
    assert.equal(client.connection, 'error');
    assert.equal(rec.lastUpdate().state, null);
    assert.equal(rec.lastUpdate().error, 'device unplugged');
    client.stop();
});

test('protocol error surfaces via onError without dropping the connection', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    t.emitMessage(LIVE);
    t.emitMessage({ type: 'error', error: 'unknown action: foo' });
    assert.equal(client.connection, 'connected');
    assert.deepEqual(rec.errors, ['unknown action: foo']);
    client.stop();
});

test('freshness watchdog tears down a silently-wedged daemon and reconnects', () => {
    const { clock, transport, rec, client } = makeClient();
    client.start();
    const t0 = transport.last();
    t0.emitOpen();
    t0.emitMessage(LIVE);
    assert.equal(client.connection, 'connected');

    // No further state for longer than the watchdog window.
    clock.tick(1000);
    assert.equal(client.connection, 'error');
    assert.equal(rec.lastUpdate().state, null);
    assert.equal(t0.closed, true); // old socket torn down

    // Backoff elapses -> a brand new transport is created.
    const createdBefore = transport.created.length;
    clock.tick(500);
    assert.equal(transport.created.length, createdBefore + 1);
    const t1 = transport.last();
    t1.emitOpen();
    t1.emitMessage(MUTED);
    assert.equal(client.connection, 'connected');
    assert.equal(rec.lastUpdate().state.muted, true);
    client.stop();
});

test('watchdog is reset by each fresh state (no false teardown while live)', () => {
    const { clock, transport, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    for (let i = 0; i < 5; i++) {
        t.emitMessage(LIVE);
        clock.tick(900); // under the 1000ms window each time
        assert.equal(client.connection, 'connected');
    }
    client.stop();
});

test('socket close schedules a reconnect with backoff', () => {
    const { clock, transport, client } = makeClient();
    client.start();
    const t0 = transport.last();
    t0.emitOpen();
    t0.emitMessage(LIVE);

    t0.emitClose('server went away');
    assert.equal(client.connection, 'error');

    const before = transport.created.length;
    clock.tick(500); // base backoff
    assert.equal(transport.created.length, before + 1);
    client.stop();
});

test('stop(): tears down timers, closes socket, ignores late callbacks', () => {
    const { clock, transport, rec, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    t.emitMessage(LIVE);
    assert.equal(client.connection, 'connected');

    client.stop();
    assert.equal(client.connection, 'disconnected');
    assert.equal(rec.lastUpdate().state, null);
    assert.equal(t.closed, true);
    assert.equal(clock.pending(), 0, 'no timers left running after stop');

    // Any late frame or close from the dead socket must be inert.
    const updatesAfterStop = rec.updates.length;
    t.emitMessage(MUTED);
    t.emitClose('late');
    clock.tick(10000);
    assert.equal(rec.updates.length, updatesAfterStop);
    assert.equal(client.connection, 'disconnected');
});

test('setUrl(): switches endpoint and old socket callbacks cannot resurrect state', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    const t0 = transport.last();
    t0.emitOpen();
    t0.emitMessage(LIVE);
    assert.equal(client.connection, 'connected');

    client.setUrl('http://localhost:9099');
    assert.equal(t0.closed, true);
    // A late frame from the old transport must be ignored.
    const n = rec.updates.length;
    t0.emitMessage(MUTED);
    assert.ok(client.connection === 'connecting' || client.connection === 'disconnected');

    // The new transport targets the new endpoint.
    const t1 = transport.last();
    assert.notEqual(t1, t0);
    assert.equal(t1.opts.wsUrl, 'ws://localhost:9099/ws');
    t1.emitOpen();
    t1.emitMessage(MUTED);
    assert.equal(client.connection, 'connected');
    assert.ok(rec.updates.length > n);
    client.stop();
});

test('invalid endpoint fails fast without reconnect storms', () => {
    const { clock, transport, rec, client } = makeClient({ url: 'http://example.com:9000' });
    client.start();
    assert.equal(client.connection, 'error');
    assert.ok(rec.errors.some((e) => /loopback/i.test(e)));
    assert.equal(transport.created.length, 0); // never attempted a socket
    clock.tick(60000);
    assert.equal(transport.created.length, 0); // and does not spin
    client.stop();
});

// ---------------------------------------------------------------------------
// send()
// ---------------------------------------------------------------------------

test('send(): false before a socket is open, true once open, correct wire payload', () => {
    const { transport, client } = makeClient();
    client.start();
    // not open yet
    assert.equal(client.send('set_mute', { value: true }), false);

    const t = transport.last();
    t.emitOpen();
    assert.equal(client.send('set_mute', {value: true}), false);
    t.emitMessage(LIVE);
    assert.equal(client.send('set_mute', { value: true }), true);
    assert.equal(t.sent.length, 1);
    assert.deepEqual(JSON.parse(t.sent[0]), { action: 'set_mute', params: { value: true } });

    // get_state with default params
    assert.equal(client.send('get_state'), true);
    assert.deepEqual(JSON.parse(t.sent[1]), { action: 'get_state', params: {} });
    client.stop();
});

test('send(): refuses destructive actions and reports via onError', () => {
    const { transport, rec, client } = makeClient();
    client.start();
    transport.last().emitOpen();
    assert.equal(client.send('factory_reset'), false);
    assert.equal(client.send('reboot', {}), false);
    assert.equal(transport.last().sent.length, 0);
    assert.ok(rec.errors.length >= 2);
    assert.ok(rec.errors.some((e) => /factory_reset/i.test(e)));
    assert.ok(rec.errors.some((e) => /destructive/i.test(e)));
    client.stop();
});

test('send(): false after stop', () => {
    const { transport, client } = makeClient();
    client.start();
    transport.last().emitOpen();
    client.stop();
    assert.equal(client.send('set_mute', { value: false }), false);
});

test('send(): factory_reset blocked by default, allowed only with opt-in', () => {
    // default client: refused
    const a = makeClient();
    a.client.start();
    a.transport.last().emitOpen();
    assert.equal(a.client.send('factory_reset'), false);
    assert.equal(a.transport.last().sent.length, 0);
    a.client.stop();

    // opt-in client (native app path)
    const b = makeClient({ allowFactoryReset: true });
    b.client.start();
    b.transport.last().emitOpen();
    b.transport.last().emitMessage(LIVE);
    assert.equal(b.client.send('factory_reset'), true);
    assert.deepEqual(JSON.parse(b.transport.last().sent[0]), { action: 'factory_reset', params: {} });
    // but other destructive actions remain blocked even with the opt-in
    assert.equal(b.client.send('reboot'), false);
    b.client.stop();
});

test('send(): RGB LED command reaches the wire intact', () => {
    const { transport, client } = makeClient();
    client.start();
    const t = transport.last();
    t.emitOpen();
    t.emitMessage(LIVE);
    assert.equal(client.send('set_led_solid_color', { rgb: [178, 255, 51] }), true);
    assert.deepEqual(JSON.parse(t.sent[0]), {
        action: 'set_led_solid_color', params: { rgb: [178, 255, 51] },
    });

    client.stop();
});

test('status heartbeats without fresh state cannot keep old mute alive', () => {
    const {clock, transport, rec, client} = makeClient();
    client.start();
    const socket = transport.last();
    socket.emitOpen();
    socket.emitMessage(LIVE);
    for (let i = 0; i < 4; i++) {
        clock.tick(200);
        socket.emitMessage({type: 'status', connected: true});
    }
    clock.tick(200);
    assert.equal(client.connection, 'error');
    assert.equal(rec.lastUpdate().state, null);
    assert.equal(socket.closed, true);
    client.stop();
});
