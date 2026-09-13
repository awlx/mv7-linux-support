import test from 'node:test';
import assert from 'node:assert/strict';
import {advancePeakHold, meterDisplay} from '../state.js';

const reading = (peak, overrides = {}) => ({
    available: true, peak_dbfs: peak, rms_dbfs: Math.max(-90, peak - 3),
    clipping: peak === 0, source: 'mv7', ...overrides,
});
const close = (actual, expected) => assert.ok(Math.abs(actual - expected) < 1e-9,
    `expected ${expected}, got ${actual}`);

test('peak hold attacks immediately without changing the live reading', () => {
    const live = reading(-30);
    const initial = advancePeakHold(null, live, 0);
    assert.equal(initial.peak_dbfs, -30);
    assert.equal(initial.holdUntil, 1000);
    const higher = advancePeakHold(initial, reading(-6), 100);
    assert.equal(higher.peak_dbfs, -6);
    assert.equal(higher.holdUntil, 1100);
    assert.deepEqual(live, reading(-30));
    assert.equal(meterDisplay(live).fraction, 0.5);
});

test('peak holds for exactly one second, then decays at 12 dB per second', () => {
    let held = advancePeakHold(null, reading(-6), 0);
    held = advancePeakHold(held, reading(-60), 999);
    assert.equal(held.peak_dbfs, -6);
    held = advancePeakHold(held, reading(-60), 1000);
    assert.equal(held.peak_dbfs, -6);
    held = advancePeakHold(held, reading(-60), 1500);
    assert.equal(held.peak_dbfs, -12);
    held = advancePeakHold(held, reading(-60), 2000);
    assert.equal(held.peak_dbfs, -18);
});

test('peak decay depends on elapsed time, not frame rate', () => {
    for (const interval of [16, 50, 100, 333, 2000]) {
        let held = advancePeakHold(null, reading(-6), 0);
        for (let time = interval; time < 2000; time += interval)
            held = advancePeakHold(held, reading(-60), time);
        held = advancePeakHold(held, reading(-60), 2000);
        close(held.peak_dbfs, -18);
    }
});

test('equal peaks refresh the hold and higher peaks interrupt decay', () => {
    let held = advancePeakHold(null, reading(-6), 0);
    held = advancePeakHold(held, reading(-6), 900);
    assert.equal(held.holdUntil, 1900);
    held = advancePeakHold(held, reading(-60), 1500);
    assert.equal(held.peak_dbfs, -6);
    held = advancePeakHold(held, reading(-60), 2400);
    assert.equal(held.peak_dbfs, -12);
    held = advancePeakHold(held, reading(-3), 2500);
    assert.equal(held.peak_dbfs, -3);
    assert.equal(held.holdUntil, 3500);
});

test('held peak never falls below the current signal or the numerical floor', () => {
    const held = advancePeakHold(null, reading(-6), 0);
    assert.equal(advancePeakHold(held, reading(-20), 5000).peak_dbfs, -20);
    const silence = advancePeakHold(held, reading(-90), 10000);
    assert.equal(silence.peak_dbfs, -90);
    assert.equal(meterDisplay(silence).fraction, 0);
    assert.equal(meterDisplay(silence).text, '<= -90 dBFS');
});

test('clipping is held briefly, clears during decay, and is not inferred from rounding', () => {
    let held = advancePeakHold(null, reading(0), 0);
    held = advancePeakHold(held, reading(-30), 999);
    assert.equal(held.clipping, true);
    held = advancePeakHold(held, reading(-30), 1100);
    close(held.peak_dbfs, -1.2);
    assert.equal(held.clipping, false);
    const nearFullScale = advancePeakHold(null, reading(-0.1), 0);
    assert.equal(nearFullScale.clipping, false);
    assert.doesNotMatch(meterDisplay(nearFullScale).detail, /CLIPPING/);
});

test('unavailable, disabled, malformed and changed-source readings clear old peaks', () => {
    const held = advancePeakHold(null, reading(0), 0);
    for (const invalid of [null, {available: false, error: 'Live input meter disabled'},
        {available: false, error: 'No fresh input-level reading'}, reading(NaN)]) {
        const cleared = advancePeakHold(held, invalid, 100);
        assert.equal(cleared.available, false);
        assert.equal(cleared.peak_dbfs, undefined);
        assert.equal(advancePeakHold(cleared, reading(-40), 200).peak_dbfs, -40);
    }
    const changed = advancePeakHold(held, reading(-40, {source: 'reconnected-mv7'}), 100);
    assert.equal(changed.peak_dbfs, -40);
    assert.equal(changed.clipping, false);
});

test('backwards timestamps cannot raise a peak or accelerate later decay', () => {
    let held = advancePeakHold(null, reading(-6), 0);
    held = advancePeakHold(held, reading(-60), 2000);
    held = advancePeakHold(held, reading(-60), 1500);
    assert.equal(held.peak_dbfs, -18);
    assert.equal(held.updatedAt, 2000);
    close(advancePeakHold(held, reading(-60), 2100).peak_dbfs, -19.2);
    for (const invalid of [NaN, Infinity, -1])
        assert.throws(() => advancePeakHold(null, reading(-6), invalid), RangeError);
});
