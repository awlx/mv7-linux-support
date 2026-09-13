#!/usr/bin/env -S gjs -m
// SPDX-License-Identifier: GPL-3.0-only
//
// Optional GJS smoke test for the MV7+ daemon client.  This is NOT part of the
// Node suite (state.test.js / daemonClient.test.js) which is the authoritative,
// required coverage.  Its purpose is to validate, inside a real GJS + libsoup3
// runtime, that:
//
//   1. state.js and daemonClient.js load as GJS ESM with no import errors;
//   2. the pure helpers behave identically under GJS;
//   3. DaemonClient drives a full lifecycle through the injected test harness;
//   4. the real libsoup3 transport wires up (websocket_connect_async/finish),
//      GLib timers arm, and a failed connect to a dead port flows through
//      onUpdate/onError and reconnect scheduling without throwing.
//
// Run:  gjs -m tests/gjs-smoke.js
//   (from the gnome-extension/ directory, or adjust the relative imports)

import GLib from 'gi://GLib';

import {
    parseEndpoint,
    buildCommand,
    normalizeState,
    coerceBool,
} from '../state.js';
import { DaemonClient } from '../daemonClient.js';

let failures = 0;
function check(cond, msg) {
    if (cond) {
        print(`  ok  - ${msg}`);
    } else {
        failures++;
        printerr(`  FAIL- ${msg}`);
    }
}

// --- 1 & 2: pure helpers -----------------------------------------------------
print('state.js pure helpers under GJS:');
check(parseEndpoint('http://127.0.0.1:8090').wsUrl === 'ws://127.0.0.1:8090/ws',
    'endpoint maps to loopback /ws');
check(parseEndpoint('http://example.com').valid === false, 'rejects remote host');
check(buildCommand('factory_reset').valid === false, 'refuses factory_reset');
check(buildCommand('set_mute', { value: true }).valid === true, 'builds set_mute');
check(coerceBool('weird') === null, 'malformed bool -> null (never false)');
check(normalizeState({ muted: 'weird', gain_db: 10 }).muted === null,
    'malformed mute normalizes to null');
check(normalizeState({ muted: true, gain_db: 24.5, hpf: 1 }).gainDb === 24.5,
    'valid snapshot normalizes gain');

// --- 3: injected-harness lifecycle ------------------------------------------
print('DaemonClient lifecycle via injected harness:');
{
    // Minimal virtual clock + fake transport (mirrors tests/helpers.js).
    const timers = new Map();
    let tid = 0;
    const clock = {
        setTimeout(fn) {
            const id = ++tid;
            timers.set(id, fn);
            return id;
        },
        clearTimeout(id) {
            timers.delete(id);
        },
    };
    let last = null;
    const transportFactory = (opts) => {
        last = { opts, sent: [], closed: false,
            send(t) { this.sent.push(t); return true; },
            close() { this.closed = true; } };
        return last;
    };
    const updates = [];
    const client = new DaemonClient({
        url: 'http://127.0.0.1:8090',
        onUpdate: (u) => updates.push(u),
        onError: () => {},
        clock,
        transportFactory,
    });
    client.start();
    check(updates.length >= 1 && updates[updates.length - 1].connection === 'connecting',
        'start -> connecting');
    last.opts.onOpen();
    check(client.connection === 'connecting', 'socket-open alone is not connected');
    last.opts.onMessage(JSON.stringify({ type: 'state', state: { muted: true, gain_db: 12 } }));
    check(client.connection === 'connected', 'valid state -> connected');
    check(updates[updates.length - 1].state.muted === true, 'connected snapshot has muted=true');
    check(client.send('set_mute', { value: false }) === true, 'send returns true when open');
    check(last.sent.length === 1, 'command reached the wire');
    client.stop();
    check(client.connection === 'disconnected', 'stop -> disconnected');
    check(last.closed === true, 'stop closes the transport');
}

// --- 4: real libsoup3 transport against a dead port --------------------------
print('Real libsoup3 transport (dead-port connect):');
{
    const loop = new GLib.MainLoop(null, false);
    const seen = [];
    // Port 1 is (essentially always) closed; the connect must fail and flow
    // through the client's close/reconnect path rather than throwing.
    const client = new DaemonClient({
        url: 'http://127.0.0.1:1',
        watchdogMs: 2000,
        backoff: { base: 200, factor: 2, cap: 1000, jitter: 0 },
        onUpdate: (u) => seen.push(u.connection),
        onError: () => {},
    });

    let done = false;
    const finish = () => {
        if (done)
            return;
        done = true;
        client.stop();
        check(seen.includes('connecting'), 'real client emitted connecting');
        check(client.connection === 'disconnected', 'stop cleanly disconnects real client');
        loop.quit();
    };

    // Bound the whole thing so a hang cannot wedge CI.
    GLib.timeout_add(GLib.PRIORITY_DEFAULT, 4000, () => {
        finish();
        return GLib.SOURCE_REMOVE;
    });

    client.start();
    // Give the async connect + one reconnect attempt time to run, then finish.
    GLib.timeout_add(GLib.PRIORITY_DEFAULT, 1500, () => {
        finish();
        return GLib.SOURCE_REMOVE;
    });

    loop.run();
}

print(failures === 0 ? '\nSMOKE OK' : `\nSMOKE FAILED (${failures})`);
if (failures > 0)
    imports.system.exit(1);
