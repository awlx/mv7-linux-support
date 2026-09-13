// SPDX-License-Identifier: GPL-3.0-only
//
// Pure, dependency-free state / protocol helpers for the MV7+ GNOME daemon
// client.  Everything in this module is written against ECMAScript primitives
// only (no `URL`, no GJS imports, no Node built-ins) so that it runs unchanged
// under both GJS (GNOME Shell) and the Node built-in test runner.
//
// The GNOME Shell indicator is only ever allowed to show a *live* device state
// when the daemon has reported a genuine, well-formed snapshot.  The helpers
// here are deliberately conservative: anything unknown, malformed, or missing
// is surfaced as `null`/unknown rather than being coerced into a concrete
// (and potentially wrong) value such as "not muted".

'use strict';

export const DEFAULT_ENDPOINT = 'http://127.0.0.1:8090';

// WebSocket path served by the daemon.  The HTTP endpoint's authority is reused
// verbatim and only the scheme/path change.
export const WS_PATH = '/ws';

// Connection phases surfaced to the UI via `onUpdate({connection})`.
export const Connection = Object.freeze({
    CONNECTING: 'connecting',
    CONNECTED: 'connected',
    DISCONNECTED: 'disconnected',
    ERROR: 'error',
});

// -------------------------------------------------------------------------
// Endpoint validation
// -------------------------------------------------------------------------
//
// The client only ever talks to a loopback daemon.  We refuse anything that
// could be used to exfiltrate to, or be driven by, a remote host: non-loopback
// authorities, embedded credentials, query strings, fragments and non-root
// paths are all rejected.  Parsing is done by hand because GJS does not expose
// a standards `URL` global and we must behave identically in Node.

// Matches: scheme "://" authority [ path ] [ "?" query ] [ "#" fragment ]
const ENDPOINT_RE = /^([a-zA-Z][a-zA-Z0-9+.-]*):\/\/([^/?#]*)([^?#]*)(\?[^#]*)?(#.*)?$/;

function isLoopbackIPv4(host) {
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
    if (!m)
        return false;
    const octets = m.slice(1).map(Number);
    if (octets.some(o => o > 255))
        return false;
    // Entire 127.0.0.0/8 block is loopback.
    return octets[0] === 127;
}

function isLoopbackIPv6(host) {
    // Strip an optional zone id (e.g. "::1%lo0") before comparing.
    const bare = host.split('%')[0].toLowerCase();
    if (bare === '::1')
        return true;
    // Fully expanded loopback address.
    if (/^0*:0*:0*:0*:0*:0*:0*:0*1$/.test(bare))
        return true;
    // IPv4-mapped loopback, e.g. ::ffff:127.0.0.1
    const mapped = /^::ffff:(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$/.exec(bare);
    if (mapped)
        return isLoopbackIPv4(mapped[1]);
    return false;
}

export function isLoopbackHost(host) {
    if (typeof host !== 'string' || host.length === 0)
        return false;
    const lower = host.toLowerCase();
    if (lower === 'localhost' || lower === 'localhost.localdomain')
        return true;
    if (isLoopbackIPv4(lower))
        return true;
    if (isLoopbackIPv6(lower))
        return true;
    return false;
}

// Split an authority ("host", "host:port", "[::1]", "[::1]:port") into its
// host and port pieces without interpreting credentials (callers must have
// already rejected any userinfo).
function splitAuthority(authority) {
    let host;
    let port = '';
    if (authority.startsWith('[')) {
        const end = authority.indexOf(']');
        if (end === -1)
            return null;
        host = authority.slice(1, end);
        const rest = authority.slice(end + 1);
        if (rest.startsWith(':'))
            port = rest.slice(1);
        else if (rest.length > 0)
            return null;
    } else {
        const idx = authority.lastIndexOf(':');
        if (idx === -1) {
            host = authority;
        } else {
            host = authority.slice(0, idx);
            port = authority.slice(idx + 1);
        }
    }
    return { host, port };
}

/**
 * Validate a user-supplied endpoint and derive the matching WebSocket URL.
 *
 * @param {string} [input] endpoint; empty/undefined uses DEFAULT_ENDPOINT.
 * @returns {{valid: boolean, error: string|null, httpUrl: string|null,
 *            wsUrl: string|null, scheme: string|null, host: string|null,
 *            port: string|null, secure: boolean}}
 */
export function parseEndpoint(input) {
    const fail = (error) => ({
        valid: false, error, httpUrl: null, wsUrl: null,
        scheme: null, host: null, port: null, secure: false,
    });

    let raw = input == null ? '' : String(input).trim();
    if (raw === '')
        raw = DEFAULT_ENDPOINT;

    const m = ENDPOINT_RE.exec(raw);
    if (!m)
        return fail('Endpoint is not a valid absolute URL');

    const scheme = m[1].toLowerCase();
    const authority = m[2];
    const path = m[3] || '';
    const query = m[4] || '';
    const fragment = m[5] || '';

    if (scheme !== 'http' && scheme !== 'https')
        return fail(`Unsupported scheme "${scheme}" (expected http or https)`);
    if (authority.includes('@'))
        return fail('Endpoint must not contain credentials');
    if (query)
        return fail('Endpoint must not contain a query string');
    if (fragment)
        return fail('Endpoint must not contain a fragment');
    if (path !== '' && path !== '/')
        return fail('Endpoint must not contain a path');

    const parts = splitAuthority(authority);
    if (!parts || parts.host === '')
        return fail('Endpoint is missing a host');
    if (parts.port !== '' && !/^\d+$/.test(parts.port))
        return fail('Endpoint has an invalid port');
    if (parts.port !== '') {
        const p = Number(parts.port);
        if (p < 1 || p > 65535)
            return fail('Endpoint port is out of range');
    }
    if (!isLoopbackHost(parts.host))
        return fail(`Endpoint host "${parts.host}" is not loopback`);

    const secure = scheme === 'https';
    const hostForUrl = parts.host.includes(':') && !parts.host.startsWith('[')
        ? `[${parts.host}]`
        : parts.host;
    const authorityOut = parts.port ? `${hostForUrl}:${parts.port}` : hostForUrl;
    const wsScheme = secure ? 'wss' : 'ws';

    return {
        valid: true,
        error: null,
        httpUrl: `${scheme}://${authorityOut}/`,
        wsUrl: `${wsScheme}://${authorityOut}${WS_PATH}`,
        scheme,
        host: parts.host,
        port: parts.port || null,
        secure,
    };
}

// -------------------------------------------------------------------------
// Outbound command building
// -------------------------------------------------------------------------
//
// Commands mirror the web UI's `{action, params}` shape.  We never expose a
// factory-reset (or any other destructive) action through this helper.  The
// allow-list documents the harmless setters and queries we expect; unknown
// actions are rejected rather than forwarded to hardware.

// Harmless queries/setters exposed by the daemon (see internal/server
// handleAction).  Params are always `{value}` (or `{rgb}` for LED colours);
// building the params object is the caller's job.
export const KNOWN_ACTIONS = Object.freeze([
    'get_state',
    'subscribe_meter',
    'set_gain',
    'set_gain_locked',
    'set_mute',
    'set_hpf',
    'set_limiter',
    'set_compressor',
    'set_denoiser',
    'set_auto_level',
    'set_mic_mix',
    'set_playback_mix',
    'set_popper_stopper',
    'set_tone',
    'set_mute_btn',
    'set_reverb_output',
    'set_reverb_type',
    'set_reverb_intensity',
    'set_reverb_monitor',
    'set_led_behavior',
    'set_led_brightness',
    'set_led_live_theme',
    'set_led_solid_theme',
    'set_led_pulsing_theme',
    'set_led_solid_color',
    'set_led_pulsing_color',
    'set_led_live_edge',
    'set_led_live_middle',
    'set_led_live_interior',
]);

// Destructive / irreversible actions that must never be issued from this
// client. `factory_reset` is handled separately (opt-in) and is intentionally
// NOT listed here.
export const DENIED_ACTIONS = Object.freeze([
    'reset',
    'reset_defaults',
    'reboot',
    'firmware_update',
    'flash',
    'erase',
]);

const KNOWN_SET = new Set(KNOWN_ACTIONS);
const DENIED_SET = new Set(DENIED_ACTIONS);
const FACTORY_RESET = new Set(['factory_reset', 'factoryreset']);

function normalizeActionName(action) {
    return String(action).trim().toLowerCase().replace(/[\s-]+/g, '_');
}

/**
 * Build (and validate) an outbound command envelope.
 *
 * `factory_reset` is refused unless `options.allowFactoryReset` is explicitly
 * true — the GNOME Shell indicator never enables it, while the native GTK4 app
 * may opt in *after* its own Adwaita confirmation dialog. All other destructive
 * actions are always refused. RGB setters (`{rgb:[r,g,b]}`) and scalar setters
 * (`{value}`) both pass through unchanged.
 *
 * @param {string} action
 * @param {object} [params]
 * @param {{allowFactoryReset?: boolean}} [options]
 * @returns {{valid: boolean, error: string|null,
 *            payload: {action: string, params: object}|null,
 *            known: boolean}}
 */
export function buildCommand(action, params = {}, options = {}) {
    const fail = (error) => ({ valid: false, error, payload: null, known: false });

    if (typeof action !== 'string' || action.trim() === '')
        return fail('Command action must be a non-empty string');
    if (params == null)
        params = {};
    if (typeof params !== 'object' || Array.isArray(params))
        return fail('Command params must be a plain object');
    if (action === 'subscribe_meter' && typeof params.enabled !== 'boolean')
        return fail('subscribe_meter requires a boolean enabled parameter');

    const canonical = normalizeActionName(action);
    if (FACTORY_RESET.has(canonical)) {
        if (!options.allowFactoryReset)
            return fail('Refusing factory_reset (requires explicit opt-in)');
    } else if (DENIED_SET.has(canonical)) {
        return fail(`Refusing to send destructive action "${action}"`);
    } else if (!KNOWN_SET.has(action)) {
        return fail(`Unknown command action "${action}"`);
    }

    // Ensure params is JSON-serializable; reject functions/cycles early.
    let safeParams;
    try {
        safeParams = JSON.parse(JSON.stringify(params));
    } catch {
        return fail('Command params are not JSON-serializable');
    }

    return {
        valid: true,
        error: null,
        payload: { action: action.trim(), params: safeParams },
        known: KNOWN_SET.has(canonical),
    };
}

// -------------------------------------------------------------------------
// Inbound value coercion
// -------------------------------------------------------------------------

const TRUE_TOKENS = new Set(['true', 'on', '1', 'yes', 'muted', 'mute', 'enabled', 'locked']);
const FALSE_TOKENS = new Set(['false', 'off', '0', 'no', 'unmuted', 'live', 'disabled', 'unlocked']);

/**
 * Coerce an unknown value into a strict boolean, or `null` when it cannot be
 * confidently interpreted.  Critically, a malformed value NEVER collapses to
 * `false` — an unreadable mute flag must not make the microphone look live.
 */
export function coerceBool(value) {
    if (typeof value === 'boolean')
        return value;
    if (typeof value === 'number') {
        if (value === 1)
            return true;
        if (value === 0)
            return false;
        return null;
    }
    if (typeof value === 'string') {
        const t = value.trim().toLowerCase();
        if (TRUE_TOKENS.has(t))
            return true;
        if (FALSE_TOKENS.has(t))
            return false;
        return null;
    }
    return null;
}

/**
 * Coerce a gain reading into a number in the valid 0..36 dB range (fractional
 * .5 steps supported).  Out-of-range or unreadable values become `null`.
 */
export function coerceGain(value) {
    let n = null;
    if (typeof value === 'number')
        n = value;
    else if (typeof value === 'string' && value.trim() !== '')
        n = Number(value);
    if (n == null || !Number.isFinite(n))
        return null;
    if (n < 0 || n > 36)
        return null;
    return n;
}

/**
 * Coerce a small unsigned enum field (e.g. hpf 0..2, compressor 0..3) into a
 * non-negative integer, or `null` when unreadable/negative.  Unknown-but-valid
 * higher values are preserved so future firmware modes are not rejected.
 */
export function coerceUint(value) {
    let n = null;
    if (typeof value === 'number')
        n = value;
    else if (typeof value === 'boolean')
        n = value ? 1 : 0;
    else if (typeof value === 'string' && value.trim() !== '')
        n = Number(value);
    if (n == null || !Number.isFinite(n) || n < 0 || !Number.isInteger(n))
        return null;
    return n;
}

// Candidate key lists.  Field names are matched case-insensitively and across
// snake_case / camelCase so the helper survives naming churn in the daemon.
// The daemon's canonical json tag is listed first in each group.
const MIC_MUTE_KEYS = ['muted', 'mic_muted', 'mic_mute', 'micmute', 'mute'];
const GAIN_KEYS = ['gain_db', 'gaindb', 'gain', 'input_gain', 'inputgain'];
const GAIN_LOCK_KEYS = ['gain_locked', 'gainlocked', 'gain_lock', 'locked'];
const AUTO_LEVEL_KEYS = ['auto_level', 'autolevel'];
const HPF_KEYS = ['hpf', 'high_pass', 'highpass', 'high_pass_filter'];
const COMPRESSOR_KEYS = ['compressor', 'comp'];
const LIMITER_KEYS = ['limiter'];
const DENOISER_KEYS = ['denoiser'];

function lookup(obj, keys) {
    // Build a lower-cased key index once per call.
    const index = new Map();
    for (const k of Object.keys(obj))
        index.set(k.toLowerCase(), obj[k]);
    for (const key of keys) {
        if (index.has(key))
            return { found: true, value: index.get(key) };
    }
    return { found: false, value: undefined };
}

/**
 * Normalize a raw device-state object into a safe snapshot.
 *
 * A usable snapshot requires the daemon's canonical boolean mute and numeric
 * gain fields. Optional and future fields are preserved verbatim in `raw`.
 *
 * @param {*} state
 * @returns {{valid: boolean, muted: boolean|null, gainDb: number|null,
 *            gainLocked: boolean|null, autoLevel: boolean|null,
 *            hpf: number|null, compressor: number|null, limiter: boolean|null,
 *            denoiser: boolean|null, raw: object|null}}
 */
export function normalizeState(state) {
    const empty = {
        valid: false, muted: null, gainDb: null, gainLocked: null,
        autoLevel: null, hpf: null, compressor: null, limiter: null,
        denoiser: null, raw: null,
    };
    if (state == null || typeof state !== 'object' || Array.isArray(state))
        return empty;
    if (Object.keys(state).length === 0)
        return empty;

    const micMute = lookup(state, MIC_MUTE_KEYS);
    const gain = lookup(state, GAIN_KEYS);
    const gainLock = lookup(state, GAIN_LOCK_KEYS);
    const autoLevel = lookup(state, AUTO_LEVEL_KEYS);
    const hpf = lookup(state, HPF_KEYS);
    const compressor = lookup(state, COMPRESSOR_KEYS);
    const limiter = lookup(state, LIMITER_KEYS);
    const denoiser = lookup(state, DENOISER_KEYS);

    return {
        valid: typeof state.muted === 'boolean' && typeof state.gain_db === 'number' &&
            coerceGain(state.gain_db) !== null,
        // When the key is absent we report `null` (unknown); when present but
        // malformed, coerceBool also returns `null` — never a bogus `false`.
        muted: micMute.found ? coerceBool(micMute.value) : null,
        gainDb: gain.found ? coerceGain(gain.value) : null,
        gainLocked: gainLock.found ? coerceBool(gainLock.value) : null,
        autoLevel: autoLevel.found ? coerceBool(autoLevel.value) : null,
        // hpf (0=off,1=75Hz,2=150Hz) and compressor (0..3) are numeric enums.
        hpf: hpf.found ? coerceUint(hpf.value) : null,
        compressor: compressor.found ? coerceUint(compressor.value) : null,
        limiter: limiter.found ? coerceBool(limiter.value) : null,
        denoiser: denoiser.found ? coerceBool(denoiser.value) : null,
        raw: state,
    };
}

// -------------------------------------------------------------------------
// Inbound message parsing
// -------------------------------------------------------------------------

/**
 * Parse a raw WebSocket text frame (or already-decoded object) into a
 * discriminated event.  Never throws.
 *
 * @param {string|object} raw
 * @returns {{kind: 'state'|'error'|'status'|'unknown'|'invalid', ...}}
 */
export function parseServerMessage(raw) {
    let msg = raw;
    if (typeof raw === 'string') {
        try {
            msg = JSON.parse(raw);
        } catch {
            return { kind: 'invalid', error: 'Malformed JSON frame', raw };
        }
    }
    if (msg == null || typeof msg !== 'object' || Array.isArray(msg))
        return { kind: 'invalid', error: 'Frame is not a JSON object', raw };

    switch (msg.type) {
    case 'meter':
        return {kind: 'meter', meter: parseMeter(msg.meter)};
    case 'state': {
        const normalized = normalizeState(msg.state);
        return { kind: 'state', normalized, state: msg.state };
    }
    case 'error':
        return {
            kind: 'error',
            error: msg.error == null ? 'Unknown error' : String(msg.error),
        };
    case 'status':
        return {
            kind: 'status',
            connected: coerceBool(msg.connected) === true,
            meterSupported: msg.meter_supported === true,
            error: msg.error == null ? null : String(msg.error),
        };
    default:
        return { kind: 'unknown', type: msg.type, raw: msg };
    }

}

export function parseMeter(value) {
    if (!value || value.available !== true)
        return {available: false, error: typeof value?.error === 'string'
            ? value.error : 'Live input level unavailable'};
    const valid = number => Number.isFinite(number) && number >= -90 && number <= 0;
    if (!valid(value.peak_dbfs) || !valid(value.rms_dbfs) ||
        value.rms_dbfs > value.peak_dbfs + 0.001 || typeof value.clipping !== 'boolean')
        return {available: false, error: 'Invalid input-level reading'};
    return {available: true, peak_dbfs: value.peak_dbfs, rms_dbfs: value.rms_dbfs,
        clipping: value.clipping, source: typeof value.source === 'string' ? value.source : ''};
}

export function meterDisplay(reading) {
    const meter = parseMeter(reading);
    if (!meter.available)
        return {text: '-- dBFS', detail: meter.error, fraction: 0, clipping: false};
    const text = meter.peak_dbfs <= -90 ? '<= -90 dBFS' : `${Math.round(meter.peak_dbfs)} dBFS`;
    return {text, fraction: Math.max(0, (meter.peak_dbfs + 60) / 60), clipping: meter.clipping,
        detail: `${meter.clipping ? 'CLIPPING · ' : ''}Peak ${meter.peak_dbfs.toFixed(1)} dBFS · RMS ${meter.rms_dbfs.toFixed(1)} dBFS`};
}

export function advancePeakHold(previous, reading, nowMs) {
    if (!Number.isFinite(nowMs) || nowMs < 0)
        throw new RangeError('Peak hold requires a finite, non-negative monotonic timestamp');
    const meter = parseMeter(reading);
    if (!meter.available)
        return meter;
    if (!previous?.available || previous.source !== meter.source)
        return {...meter, holdUntil: nowMs + 1000, updatedAt: nowMs};

    const updatedAt = Math.max(nowMs, previous.updatedAt);
    const elapsed = Math.max(0, updatedAt - Math.max(previous.updatedAt, previous.holdUntil));
    const fallingPeak = previous.peak_dbfs - elapsed * 12 / 1000;
    if (meter.peak_dbfs >= fallingPeak)
        return {...meter, holdUntil: updatedAt + 1000, updatedAt};
    return {
        ...meter,
        peak_dbfs: fallingPeak,
        clipping: previous.clipping && elapsed === 0,
        holdUntil: previous.holdUntil,
        updatedAt,
    };
}

// -------------------------------------------------------------------------
// Connection reducer
// -------------------------------------------------------------------------
//
// A single pure reducer drives every transition the UI observes.  It is the
// authority on the invariant that a "connected" indicator requires an actually
// valid device snapshot — socket-open alone, or a bare `status:true`, is never
// enough.  Keeping it pure lets the tests exercise the whole lifecycle without
// a live socket.

export function initialConnectionState() {
    return { connection: Connection.DISCONNECTED, state: null, error: null };
}

/**
 * @param {{connection:string, state:object|null, error:string|null}} prev
 * @param {object} event one of:
 *   {type:'socket-open'} | {type:'socket-closed', error?} |
 *   {type:'watchdog'} | a parseServerMessage() result
 * @returns {{connection:string, state:object|null, error:string|null}}
 */
export function nextConnectionState(prev, event) {
    const cur = prev || initialConnectionState();

    switch (event.type || event.kind) {
    case 'connecting':
        // A connect attempt has begun; transport is not yet usable.
        return { connection: Connection.CONNECTING, state: null, error: null };

    case 'socket-open':
        // Transport is up but we have not yet seen a valid snapshot.
        return { connection: Connection.CONNECTING, state: null, error: null };

    case 'socket-closed':
        return {
            connection: event.error ? Connection.ERROR : Connection.DISCONNECTED,
            state: null,
            error: event.error || null,
        };

    case 'watchdog':
        // Freshness watchdog fired: we have gone too long without a usable
        // snapshot, so the live indicator must be torn down rather than left
        // stale forever.
        return {
            connection: Connection.ERROR,
            state: null,
            error: event.error || 'No fresh device state from daemon',
        };

    case 'status':
        if (!event.connected) {
            // Daemon lost the device: drop any stale snapshot immediately.
            return {
                connection: event.error ? Connection.ERROR : Connection.DISCONNECTED,
                state: null,
                error: event.error || null,
            };
        }
        // status:true is emitted before every state push.  If we already have
        // a healthy snapshot, keep it (avoids icon flicker); otherwise wait in
        // the connecting phase for the state that must follow.
        return {
            connection: cur.state ? Connection.CONNECTED : Connection.CONNECTING,
            state: cur.state,
            error: null,
        };

    case 'state':
        if (event.normalized && event.normalized.valid) {
            return {
                connection: Connection.CONNECTED,
                state: event.normalized.raw,
                error: null,
            };
        }
        // Invalid data cannot continue confirming a privacy-sensitive mute state.
        return {
            connection: Connection.ERROR,
            state: null,
            error: 'Malformed device state: fresh mute and gain are required',
        };

    case 'invalid':
        return {connection: Connection.ERROR, state: null, error: event.error};

    case 'error':
        // Protocol/command error: surface the message but do not tear down a
        // healthy connection — these are typically per-command failures.
        return { connection: cur.connection, state: cur.state, error: event.error };

    default:
        return cur;
    }
}

// -------------------------------------------------------------------------
// Reconnect backoff
// -------------------------------------------------------------------------

/**
 * Exponential backoff (capped, with jitter) for reconnect scheduling.
 *
 * @param {number} attempt zero-based retry counter
 * @param {{base?:number, factor?:number, cap?:number, jitter?:number}} [opts]
 * @returns {number} delay in milliseconds
 */
export function backoffDelay(attempt, opts = {}) {
    const base = opts.base ?? 1000;
    const factor = opts.factor ?? 2;
    const cap = opts.cap ?? 30000;
    const jitter = opts.jitter ?? 0.2;
    const n = Math.max(0, Math.floor(attempt));
    const raw = Math.min(cap, base * Math.pow(factor, n));
    const spread = raw * jitter;
    // Deterministic center when jitter disabled; ±spread otherwise.
    const delta = jitter > 0 ? (Math.random() * 2 - 1) * spread : 0;
    return Math.max(0, Math.round(raw + delta));
}
