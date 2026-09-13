// SPDX-License-Identifier: GPL-3.0-only
//
// Pure-logic tests for state.js — run under the Node built-in test runner:
//   node --test

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import {
    DEFAULT_ENDPOINT,
    Connection,
    parseEndpoint,
    isLoopbackHost,
    buildCommand,
    KNOWN_ACTIONS,
    coerceBool,
    coerceGain,
    coerceUint,
    normalizeState,
    parseServerMessage,
    nextConnectionState,
    initialConnectionState,
    backoffDelay,
} from '../state.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const fixture = (name) => JSON.parse(readFileSync(join(HERE, 'fixtures', name), 'utf8'));

// ---------------------------------------------------------------------------
// Endpoint validation
// ---------------------------------------------------------------------------

test('parseEndpoint: default endpoint maps to loopback ws /ws', () => {
    const r = parseEndpoint(undefined);
    assert.equal(r.valid, true);
    assert.equal(r.httpUrl, 'http://127.0.0.1:8090/');
    assert.equal(r.wsUrl, 'ws://127.0.0.1:8090/ws');
    assert.equal(r.secure, false);
    assert.equal(parseEndpoint('').wsUrl, 'ws://127.0.0.1:8090/ws');
    assert.equal(DEFAULT_ENDPOINT, 'http://127.0.0.1:8090');
});

test('parseEndpoint: accepts loopback variants and https', () => {
    assert.equal(parseEndpoint('http://localhost:8090').wsUrl, 'ws://localhost:8090/ws');
    assert.equal(parseEndpoint('http://127.0.0.5:9000').wsUrl, 'ws://127.0.0.5:9000/ws');
    assert.equal(parseEndpoint('http://[::1]:8090').wsUrl, 'ws://[::1]:8090/ws');
    assert.equal(parseEndpoint('http://127.0.0.1').wsUrl, 'ws://127.0.0.1/ws');
    const secure = parseEndpoint('https://127.0.0.1:8443');
    assert.equal(secure.valid, true);
    assert.equal(secure.wsUrl, 'wss://127.0.0.1:8443/ws');
    assert.equal(secure.secure, true);
    // trailing root slash is fine
    assert.equal(parseEndpoint('http://127.0.0.1:8090/').valid, true);
});

test('parseEndpoint: rejects non-loopback / unsafe endpoints', () => {
    const bad = [
        'http://example.com:8090',       // remote host
        'http://10.0.0.5:8090',          // private but not loopback
        'http://192.168.1.2:8090',
        'http://0.0.0.0:8090',           // wildcard, not loopback
        'ftp://127.0.0.1:8090',          // wrong scheme
        'ws://127.0.0.1:8090',           // ws scheme not accepted as endpoint
        'http://user:pass@127.0.0.1:8090', // credentials
        'http://127.0.0.1:8090/api',     // non-root path
        'http://127.0.0.1:8090/?x=1',    // query
        'http://127.0.0.1:8090/#frag',   // fragment
        'http://127.0.0.1:99999',        // port out of range
        'http://127.0.0.1:abc',          // invalid port
        'not a url',
        'http://',                       // missing host
    ];
    for (const url of bad) {
        const r = parseEndpoint(url);
        assert.equal(r.valid, false, `expected invalid: ${url}`);
        assert.ok(r.error, `expected error for: ${url}`);
        assert.equal(r.wsUrl, null);
    }
});

test('isLoopbackHost', () => {
    assert.equal(isLoopbackHost('localhost'), true);
    assert.equal(isLoopbackHost('127.0.0.1'), true);
    assert.equal(isLoopbackHost('127.255.255.254'), true);
    assert.equal(isLoopbackHost('::1'), true);
    assert.equal(isLoopbackHost('::ffff:127.0.0.1'), true);
    assert.equal(isLoopbackHost('example.com'), false);
    assert.equal(isLoopbackHost('10.0.0.1'), false);
    assert.equal(isLoopbackHost('128.0.0.1'), false);
    assert.equal(isLoopbackHost(''), false);
});

// ---------------------------------------------------------------------------
// Command building
// ---------------------------------------------------------------------------

test('buildCommand: builds known setters and get_state', () => {
    const g = buildCommand('get_state');
    assert.equal(g.valid, true);
    assert.equal(g.known, true);
    assert.deepEqual(g.payload, { action: 'get_state', params: {} });

    const mute = buildCommand('set_mute', { value: true });
    assert.equal(mute.valid, true);
    assert.equal(mute.known, true);
    assert.deepEqual(mute.payload, { action: 'set_mute', params: { value: true } });

    for (const a of KNOWN_ACTIONS)
        assert.equal(buildCommand(a, a === 'subscribe_meter' ? {enabled: true} : {}).valid,
            true, `known action ${a} should build`);
});

test('buildCommand: refuses destructive actions (incl. factory_reset by default)', () => {
    for (const a of ['factory_reset', 'FACTORY_RESET', 'factory-reset', 'reboot', 'firmware_update', ' reset ']) {
        const r = buildCommand(a, {});
        assert.equal(r.valid, false, `${a} must be refused`);
        assert.ok(r.error, `${a} error`);
        assert.equal(r.payload, null);
    }
});

test('buildCommand: factory_reset is opt-in; other destructive stay refused', () => {
    const allowed = buildCommand('factory_reset', {}, { allowFactoryReset: true });
    assert.equal(allowed.valid, true);
    assert.deepEqual(allowed.payload, { action: 'factory_reset', params: {} });
    // opt-in does not unlock other destructive actions
    assert.equal(buildCommand('reboot', {}, { allowFactoryReset: true }).valid, false);
    assert.equal(buildCommand('erase', {}, { allowFactoryReset: true }).valid, false);
});

test('buildCommand: RGB / LED params pass through unchanged', () => {
    const rgb = buildCommand('set_led_solid_color', { rgb: [178, 255, 51] });
    assert.equal(rgb.valid, true);
    assert.equal(rgb.known, true);
    assert.deepEqual(rgb.payload, { action: 'set_led_solid_color', params: { rgb: [178, 255, 51] } });
    const edge = buildCommand('set_led_live_edge', { rgb: [0, 0, 0] });
    assert.deepEqual(edge.payload.params, { rgb: [0, 0, 0] });
});

test('buildCommand: validates shape', () => {
    assert.equal(buildCommand('', {}).valid, false);
    assert.equal(buildCommand(null, {}).valid, false);
    assert.equal(buildCommand(42, {}).valid, false);
    assert.equal(buildCommand('set_mute', []).valid, false);
    assert.equal(buildCommand('set_mute', 'x').valid, false);
    // Unknown commands must not be forwarded to hardware.
    const u = buildCommand('set_future_thing', { value: 1 });
    assert.equal(u.valid, false);
    assert.equal(u.known, false);
});

// ---------------------------------------------------------------------------
// Coercion
// ---------------------------------------------------------------------------

test('coerceBool: strict, malformed never becomes false', () => {
    assert.equal(coerceBool(true), true);
    assert.equal(coerceBool(false), false);
    assert.equal(coerceBool(1), true);
    assert.equal(coerceBool(0), false);
    assert.equal(coerceBool('on'), true);
    assert.equal(coerceBool('off'), false);
    assert.equal(coerceBool('TRUE'), true);
    // malformed / unknown -> null (NOT false)
    assert.equal(coerceBool('weird'), null);
    assert.equal(coerceBool(2), null);
    assert.equal(coerceBool(null), null);
    assert.equal(coerceBool(undefined), null);
    assert.equal(coerceBool({}), null);
});

test('coerceGain: 0..36 with .5 steps, else null', () => {
    assert.equal(coerceGain(0), 0);
    assert.equal(coerceGain(36), 36);
    assert.equal(coerceGain(24.5), 24.5);
    assert.equal(coerceGain('12.5'), 12.5);
    assert.equal(coerceGain(-1), null);
    assert.equal(coerceGain(37), null);
    assert.equal(coerceGain('nope'), null);
    assert.equal(coerceGain(null), null);
});

test('coerceUint: enums, preserves higher future values, rejects bad', () => {
    assert.equal(coerceUint(0), 0);
    assert.equal(coerceUint(2), 2);
    assert.equal(coerceUint(9), 9); // future/unknown mode preserved
    assert.equal(coerceUint('3'), 3);
    assert.equal(coerceUint(-1), null);
    assert.equal(coerceUint(1.5), null);
    assert.equal(coerceUint('x'), null);
});

// ---------------------------------------------------------------------------
// State normalization (fixture-backed)
// ---------------------------------------------------------------------------

test('normalizeState: muted device fixture', () => {
    const msg = fixture('state-muted.json');
    const n = normalizeState(msg.state);
    assert.equal(n.valid, true);
    assert.equal(n.muted, true);
    assert.equal(n.gainDb, 24.5);
    assert.equal(n.gainLocked, true);
    assert.equal(n.autoLevel, true);
    assert.equal(n.hpf, 1);
    assert.equal(n.compressor, 2);
    assert.equal(n.limiter, true);
    assert.equal(n.raw.serial, 'ABC123');
});

test('normalizeState: live device fixture', () => {
    const msg = fixture('state-live.json');
    const n = normalizeState(msg.state);
    assert.equal(n.valid, true);
    assert.equal(n.muted, false);
    assert.equal(n.gainDb, 36);
    assert.equal(n.hpf, 0);
    assert.equal(n.compressor, 0);
});

test('normalizeState: representative parent fixture', () => {
    const msg = fixture('state-representative.json');
    const n = normalizeState(msg.state);
    assert.equal(n.valid, true);
    assert.equal(n.muted, true);
    assert.equal(n.gainDb, 12.5);
    assert.equal(n.denoiser, true);
});

test('normalizeState: malformed / missing mute never maps to false', () => {
    assert.equal(normalizeState({ muted: 'weird', gain_db: 10 }).muted, null);
    assert.equal(normalizeState({ muted: 2 }).muted, null);
    assert.equal(normalizeState({ gain_db: 10 }).muted, null); // missing entirely
    // Gain alone cannot confirm microphone mute.
    const n = normalizeState({ muted: 'weird', gain_db: 10 });
    assert.equal(n.valid, false);
    assert.equal(n.gainDb, 10);
});

test('normalizeState: preserves unknown/future fields, rejects non-objects', () => {
    const n = normalizeState({ muted: true, gain_db: 10, some_future_field: 123, nested: { a: 1 } });
    assert.equal(n.valid, true);
    assert.equal(n.raw.some_future_field, 123);
    assert.equal(normalizeState(null).valid, false);
    assert.equal(normalizeState('x').valid, false);
    assert.equal(normalizeState([]).valid, false);
    assert.equal(normalizeState({}).valid, false); // empty object is not a usable snapshot
});

// ---------------------------------------------------------------------------
// Message parsing
// ---------------------------------------------------------------------------

test('parseServerMessage: state / error / status / unknown / invalid', () => {
    const s = parseServerMessage(JSON.stringify(fixture('state-muted.json')));
    assert.equal(s.kind, 'state');
    assert.equal(s.normalized.muted, true);

    const e = parseServerMessage(JSON.stringify({ type: 'error', error: 'bad message: x' }));
    assert.equal(e.kind, 'error');
    assert.equal(e.error, 'bad message: x');

    const st = parseServerMessage(JSON.stringify({ type: 'status', connected: true }));
    assert.equal(st.kind, 'status');
    assert.equal(st.connected, true);
    const sf = parseServerMessage(JSON.stringify({ type: 'status', connected: false, error: 'no device' }));
    assert.equal(sf.connected, false);
    assert.equal(sf.error, 'no device');

    assert.equal(parseServerMessage('{not json').kind, 'invalid');
    assert.equal(parseServerMessage('[]').kind, 'invalid');
    assert.equal(parseServerMessage(JSON.stringify({ type: 'weird' })).kind, 'unknown');
    // accepts already-decoded objects too
    assert.equal(parseServerMessage({ type: 'status', connected: true }).connected, true);
});

test('parseServerMessage: state event with malformed mute is unusable', () => {
    const r = parseServerMessage(JSON.stringify({ type: 'state', state: { muted: 'weird', gain_db: 5 } }));
    assert.equal(r.kind, 'state');
    assert.equal(r.normalized.valid, false);
    assert.equal(r.normalized.muted, null);
});

// ---------------------------------------------------------------------------
// Connection reducer
// ---------------------------------------------------------------------------

const stateEvent = (obj) => parseServerMessage(JSON.stringify({ type: 'state', state: obj }));

test('reducer: connected requires a valid state, never socket-open alone', () => {
    let s = initialConnectionState();
    assert.equal(s.connection, Connection.DISCONNECTED);
    s = nextConnectionState(s, { type: 'connecting' });
    assert.equal(s.connection, Connection.CONNECTING);
    s = nextConnectionState(s, { type: 'socket-open' });
    assert.equal(s.connection, Connection.CONNECTING); // still not connected
    assert.equal(s.state, null);
    s = nextConnectionState(s, stateEvent({ muted: true, gain_db: 10 }));
    assert.equal(s.connection, Connection.CONNECTED);
    assert.equal(s.state.muted, true);
});

test('reducer: status:true alone does not connect; anti-flicker when already healthy', () => {
    let s = initialConnectionState();
    s = nextConnectionState(s, { type: 'socket-open' });
    s = nextConnectionState(s, { kind: 'status', connected: true, error: null });
    assert.equal(s.connection, Connection.CONNECTING); // no state yet
    assert.equal(s.state, null);

    s = nextConnectionState(s, stateEvent({ muted: false, gain_db: 20 }));
    assert.equal(s.connection, Connection.CONNECTED);
    const healthy = s.state;
    // status:true emitted before every state push must not clear healthy state
    s = nextConnectionState(s, { kind: 'status', connected: true, error: null });
    assert.equal(s.connection, Connection.CONNECTED);
    assert.equal(s.state, healthy); // same reference -> no flicker
});

test('reducer: status:false clears stale state', () => {
    let s = initialConnectionState();
    s = nextConnectionState(s, { type: 'socket-open' });
    s = nextConnectionState(s, stateEvent({ muted: true, gain_db: 10 }));
    assert.equal(s.connection, Connection.CONNECTED);
    s = nextConnectionState(s, { kind: 'status', connected: false, error: 'device unplugged' });
    assert.equal(s.connection, Connection.ERROR);
    assert.equal(s.state, null);
    assert.equal(s.error, 'device unplugged');
});

test('reducer: watchdog tears down live state', () => {
    let s = initialConnectionState();
    s = nextConnectionState(s, { type: 'socket-open' });
    s = nextConnectionState(s, stateEvent({ muted: false, gain_db: 10 }));
    assert.equal(s.connection, Connection.CONNECTED);
    s = nextConnectionState(s, { type: 'watchdog' });
    assert.equal(s.connection, Connection.ERROR);
    assert.equal(s.state, null);
    assert.match(s.error, /fresh/i);
});

test('reducer: malformed state clears last good snapshot; command error keeps connection', () => {
    let s = initialConnectionState();
    s = nextConnectionState(s, { type: 'socket-open' });
    s = nextConnectionState(s, stateEvent({ muted: true, gain_db: 10 }));
    s = nextConnectionState(s, stateEvent({}));
    assert.equal(s.connection, Connection.ERROR);
    assert.equal(s.state, null);
    s = nextConnectionState(s, stateEvent({muted: true, gain_db: 10}));
    // a protocol error surfaces but keeps the connection
    s = nextConnectionState(s, { kind: 'error', error: 'unknown action: foo' });
    assert.equal(s.connection, Connection.CONNECTED);
    assert.equal(s.error, 'unknown action: foo');
});

test('reducer: socket-closed disconnects and clears state', () => {
    let s = initialConnectionState();
    s = nextConnectionState(s, { type: 'socket-open' });
    s = nextConnectionState(s, stateEvent({ muted: true }));
    s = nextConnectionState(s, { type: 'socket-closed', error: null });
    assert.equal(s.connection, Connection.DISCONNECTED);
    assert.equal(s.state, null);
});

// ---------------------------------------------------------------------------
// Backoff
// ---------------------------------------------------------------------------

test('backoffDelay: monotonic, capped, deterministic without jitter', () => {
    const opts = { base: 1000, factor: 2, cap: 30000, jitter: 0 };
    assert.equal(backoffDelay(0, opts), 1000);
    assert.equal(backoffDelay(1, opts), 2000);
    assert.equal(backoffDelay(2, opts), 4000);
    assert.equal(backoffDelay(5, opts), 30000); // capped
    assert.equal(backoffDelay(50, opts), 30000);
    // with jitter, stays within +/- jitter band and non-negative
    for (let i = 0; i < 100; i++) {
        const d = backoffDelay(2, { base: 1000, factor: 2, cap: 30000, jitter: 0.2 });
        assert.ok(d >= 3200 && d <= 4800, `jittered delay in band: ${d}`);
    }
});
