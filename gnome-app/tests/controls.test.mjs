import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';

import {CONTROLS, commandFor, controlEnabled, validValue} from '../controls.js';

test('native controls cover every non-destructive web setter', () => {
    const web = readFileSync(new URL('../../internal/webui/web/app.js', import.meta.url), 'utf8');
    const actions = new Set([
        ...[...web.matchAll(/wire(?:Color)?\("[^"]+",\s*"([^"]+)"/g)].map(match => match[1]),
        'set_mute', 'set_auto_level',
    ]);
    assert.deepEqual(new Set(CONTROLS.map(control => control.action)), actions);
    assert.equal(new Set(CONTROLS.map(control => control.field)).size, CONTROLS.length);
});

test('unknown mute and unavailable hardware always disable controls', () => {
    for (const control of CONTROLS) {
        for (const connection of ['connecting', 'disconnected', 'error']) {
            assert.equal(controlEnabled(control, {connection, state: {muted: false}}), false);
        }
        assert.equal(controlEnabled(control, {connection: 'connected', state: null}), false);
        assert.equal(controlEnabled(control, {connection: 'connected', state: {}}), false);
    }
});

test('gain lock, Auto Level and half-decibel gain match the daemon', () => {
    const control = CONTROLS.find(item => item.field === 'gain_db');
    const state = {muted: false, gain_db: 12.5, gain_locked: false, auto_level: false};
    assert.equal(controlEnabled(control, {connection: 'connected', state}), true);
    for (const overrides of [{gain_locked: true}, {auto_level: true}, {gain_locked: undefined}]) {
        assert.equal(controlEnabled(control, {connection: 'connected', state: {...state, ...overrides}}), false);
    }
    assert.deepEqual(commandFor(control, 12.26), {action: 'set_gain', params: {value: 12.5}});
    assert.deepEqual(commandFor(control, 36), {action: 'set_gain', params: {value: 36}});
    for (const value of [-0.5, 36.5, NaN, Infinity, '12.5'])
        assert.throws(() => commandFor(control, value), /Invalid value/);
});

test('LED brightness uses device values 3 through 6', () => {
    const control = CONTROLS.find(item => item.field === 'led_brightness');
    for (const value of [3, 4, 5, 6])
        assert.equal(commandFor(control, value).params.value, value);
    for (const value of [0, 1, 2, 7, 3.5])
        assert.equal(validValue(control, value), false);
});

test('colors preserve the RGB payload and reject malformed channels', () => {
    const control = CONTROLS.find(item => item.field === 'led_solid_color');
    assert.deepEqual(commandFor(control, [178, 255, 51]),
        {action: 'set_led_solid_color', params: {rgb: [178, 255, 51]}});
    for (const value of [[1, 2], [1, 2, 256], [1, -1, 2], [0.5, 2, 3], '#abcdef'])
        assert.throws(() => commandFor(control, value), /Invalid value/);
});
