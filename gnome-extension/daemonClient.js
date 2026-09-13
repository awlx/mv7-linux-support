// SPDX-License-Identifier: GPL-3.0-only
//
// DaemonClient — a robust, cancellation-safe WebSocket client for the MV7+
// daemon, intended to run inside a GNOME Shell extension (GNOME 45–50, ESM).
//
// Design goals / invariants:
//   * Fully asynchronous: libsoup3's async WebSocket API only; no subprocesses,
//     no blocking calls on the Shell main loop.
//   * The "connected" phase requires an actual, valid device snapshot — a bare
//     open socket (or a `status:true` with no following state) is never enough.
//     No optimistic state is ever synthesised locally.
//   * A bounded freshness watchdog guarantees a silently-wedged daemon can never
//     leave a stale "live" indicator on screen indefinitely.
//   * stop() (and URL changes) deterministically tear down every handler, timer,
//     cancellable and socket; an epoch counter makes late callbacks from a dead
//     connection inert, so nothing can "resurrect" the client after disable.
//
// Testability: this module performs NO top-level GObject-Introspection imports.
// The libsoup transport and GLib timer source are loaded lazily (via dynamic
// `import('gi://...')`) the first time start() runs under GJS. Tests inject a
// fake `transportFactory` + `clock`, so the whole lifecycle can be exercised
// under the Node built-in test runner without GJS.

'use strict';

import {
    DEFAULT_ENDPOINT,
    Connection,
    parseEndpoint,
    buildCommand,
    parseServerMessage,
    nextConnectionState,
    initialConnectionState,
    backoffDelay,
} from './state.js';

// --- default GJS runtime helpers (only ever invoked under GJS) --------------

function makeGLibClock(GLib) {
    return {
        setTimeout(fn, ms) {
            return GLib.timeout_add(GLib.PRIORITY_DEFAULT, ms, () => {
                fn();
                return GLib.SOURCE_REMOVE;
            });
        },
        clearTimeout(id) {
            if (id)
                GLib.Source.remove(id);
        },
    };
}

// Builds the real libsoup3 transport. Returns a factory compatible with the
// injectable interface used by the client and the tests.
function makeLibsoupTransportFactory() {
    return function libsoupTransport(opts) {
        const { GLib, Soup } = opts.runtime;
        const session = new Soup.Session();
        let connection = null;
        let handlers = [];
        let closed = false;

        const detach = () => {
            if (connection) {
                for (const id of handlers) {
                    try {
                        connection.disconnect(id);
                    } catch (error) {
                        console.error(`MV7+ signal cleanup failed: ${error.message}`);
                    }
                }
            }
            handlers = [];
        };

        const message = Soup.Message.new('GET', opts.wsUrl);
        message.set_flags(Soup.MessageFlags.NO_REDIRECT);
        session.websocket_connect_async(
            message,
            null,
            [],
            GLib.PRIORITY_DEFAULT,
            opts.cancellable || null,
            (src, res) => {
                try {
                    connection = session.websocket_connect_finish(res);
                } catch (e) {
                    if (!closed)
                        opts.onClose(String((e && e.message) || e));
                    return;
                }
                if (closed) {
                    connection.close(Soup.WebsocketCloseCode.NORMAL, null);
                    connection = null;
                    session.abort();
                    return;
                }
                handlers.push(connection.connect('message', (_c, type, bytes) => {
                    if (type !== Soup.WebsocketDataType.TEXT)
                        return;
                    let text;
                    try {
                        text = new TextDecoder().decode(bytes.get_data());
                    } catch (error) {
                        opts.onMessage(null);
                        return;
                    }
                    opts.onMessage(text);
                }));
                handlers.push(connection.connect('closed', () => {
                    opts.onClose(null);
                }));
                handlers.push(connection.connect('error', (_c, err) => {
                    opts.onClose(String((err && err.message) || err || 'socket error'));
                }));
                opts.onOpen();
            },
        );

        return {
            send(text) {
                if (!connection || connection.get_state() !== Soup.WebsocketState.OPEN)
                    return false;
                connection.send_text(text);
                return true;
            },
            close() {
                closed = true;
                detach();
                if (connection) {
                    try {
                        connection.close(Soup.WebsocketCloseCode.NORMAL, null);
                    } catch (error) {
                        console.error(`MV7+ socket cleanup failed: ${error.message}`);
                    }
                    connection = null;
                }
                session.abort();
            },
        };
    };
}

// ---------------------------------------------------------------------------

export class DaemonClient {
    /**
     * @param {object} opts
     * @param {string} [opts.url] loopback endpoint (default 127.0.0.1:8090)
     * @param {(u:{connection:string,state:object|null,error:string|null})=>void} [opts.onUpdate]
     * @param {(message:string)=>void} [opts.onError]
     * @param {(reading:object)=>void} [opts.onMeter] opt in to numeric-only input levels
     * @param {boolean} [opts.meterEnabled] enable capture subscription (default true with onMeter)
     * @param {number} [opts.watchdogMs] freshness timeout (default 15000)
     * @param {boolean} [opts.allowFactoryReset] opt-in to permit factory_reset
     *   (native app only, after explicit confirmation); default false
     * @param {object} [opts.backoff] backoff tuning (see state.backoffDelay)
     * @param {Function} [opts.transportFactory] injected transport (tests)
     * @param {{setTimeout:Function,clearTimeout:Function}} [opts.clock] injected timers (tests)
     * @param {object} [opts.runtime] injected {GLib,Gio,Soup}
     * @param {Function} [opts.makeCancellable] injected cancellable factory
     */
    constructor(opts = {}) {
        this._url = opts.url || DEFAULT_ENDPOINT;
        this._onUpdate = typeof opts.onUpdate === 'function' ? opts.onUpdate : null;
        this._onError = typeof opts.onError === 'function' ? opts.onError : null;
        this._onMeter = typeof opts.onMeter === 'function' ? opts.onMeter : null;
        this._meterEnabled = opts.meterEnabled !== false;
        this._meterSupported = false;
        this._meterSubscribed = false;
        this._meterTimerId = 0;
        this._watchdogMs = Number.isFinite(opts.watchdogMs) ? opts.watchdogMs : 15000;
        this._backoff = opts.backoff || { base: 1000, factor: 2, cap: 30000, jitter: 0.2 };
        // Opt-in: only the native GTK4 app (after an Adwaita confirmation) sets
        // this true. The Shell indicator leaves it false so factory_reset is
        // impossible to send.
        this._allowFactoryReset = opts.allowFactoryReset === true;

        this._transportFactory = opts.transportFactory || null;
        this._clock = opts.clock || null;
        this._runtime = opts.runtime || null;
        this._makeCancellable = opts.makeCancellable || null;

        this._running = false;
        this._epoch = 0;
        this._attempt = 0;
        this._socketOpen = false;
        this._transport = null;
        this._cancellable = null;
        this._watchdogId = 0;
        this._reconnectId = 0;
        this._conn = initialConnectionState();
    }

    // --- public API --------------------------------------------------------

    start() {
        if (this._running)
            return;
        this._running = true;
        this._epoch++;
        this._attempt = 0;
        const epoch = this._epoch;

        // Fully-injected (test) path: no GI runtime needed, connect eagerly so
        // callers observe deterministic, synchronous setup.
        if (this._clock && this._transportFactory) {
            this._connect(epoch);
            return;
        }
        this._ensureRuntime().then(() => {
            if (!this._running || epoch !== this._epoch)
                return;
            this._connect(epoch);
        }).catch((e) => {
            if (this._running && epoch === this._epoch)
                this._fail(`Runtime init failed: ${(e && e.message) || e}`);
        });
    }

    stop() {
        if (!this._running)
            return;
        this._running = false;
        this._epoch++;
        this._clearTimers();
        this._cancel();
        this._closeTransport();
        this._socketOpen = false;
        this._attempt = 0;
        this._conn = { connection: Connection.DISCONNECTED, state: null, error: null };
        this._emitUpdate();
    }

    /**
     * Send a command to the daemon.
     * @returns {boolean} true iff the command was validated and handed to an
     *   open socket. Returns false for invalid/destructive actions or when no
     *   socket is currently open (no state is optimistically assumed).
     */
    send(action, params = {}) {
        const built = buildCommand(action, params, {
            allowFactoryReset: this._allowFactoryReset,
        });
        if (!built.valid) {
            if (this._onError)
                this._onError(built.error);
            return false;
        }
        if (!this._running || !this._socketOpen || !this._transport)
            return false;
        if (!['get_state', 'subscribe_meter'].includes(action) && this._conn.connection !== Connection.CONNECTED)
            return false;
        try {
            const ok = this._transport.send(JSON.stringify(built.payload));
            return ok !== false;
        } catch (e) {
            if (this._onError)
                this._onError(String((e && e.message) || e));
            return false;
        }
    }

    /** Change the endpoint; restarts the connection if currently running. */
    setUrl(url) {
        this._url = url || DEFAULT_ENDPOINT;
        this._epoch++;
        if (!this._running)
            return;
        this._clearTimers();
        this._cancel();
        this._closeTransport();
        this._socketOpen = false;
        this._attempt = 0;
        const epoch = this._epoch;
        if (this._clock && this._transportFactory) {
            this._connect(epoch);
        } else {
            this._ensureRuntime().then(() => {
                if (this._running && epoch === this._epoch)
                    this._connect(epoch);
            }).catch((e) => {
                if (this._running && epoch === this._epoch)
                    this._fail(`Runtime init failed: ${(e && e.message) || e}`);
            });
        }
    }

    /** Current connection phase (for diagnostics/tests). */
    get connection() {
        return this._conn.connection;
    }

    setMeterEnabled(enabled) {
        this._meterEnabled = enabled === true;
        this._syncMeterSubscription();
        if (!this._meterEnabled)
            this._clearMeter('Live input meter disabled');
    }

    _syncMeterSubscription() {
        if (!this._onMeter || !this._socketOpen || !this._meterSupported)
            return;
        const enabled = this._meterEnabled;
        if (enabled !== this._meterSubscribed &&
            this.send('subscribe_meter', {enabled}))
            this._meterSubscribed = enabled;
    }

    _clearMeter(error = 'Live input level unavailable') {
        if (this._meterTimerId) {
            this._clock.clearTimeout(this._meterTimerId);
            this._meterTimerId = 0;
        }
        this._onMeter?.({available: false, error});
    }

    // --- runtime bootstrap -------------------------------------------------

    async _ensureRuntime() {
        if (this._clock && this._transportFactory)
            return;
        if (!this._runtime) {
            const GLib = (await import('gi://GLib')).default;
            const Gio = (await import('gi://Gio')).default;
            const Soup = (await import('gi://Soup?version=3.0')).default;
            this._runtime = { GLib, Gio, Soup };
        }
        const { GLib, Gio } = this._runtime;
        if (!this._clock)
            this._clock = makeGLibClock(GLib);
        if (!this._makeCancellable)
            this._makeCancellable = () => new Gio.Cancellable();
        if (!this._transportFactory)
            this._transportFactory = makeLibsoupTransportFactory();
    }

    // --- connection lifecycle ----------------------------------------------

    _connect(epoch) {
        if (!this._running || epoch !== this._epoch)
            return;

        const parsed = parseEndpoint(this._url);
        if (!parsed.valid) {
            // Configuration error: do not spin on reconnects, surface and wait
            // for a corrected URL / restart.
            this._fail(parsed.error);
            return;
        }

        this._dispatch({ type: 'connecting' });

        this._cancellable = this._makeCancellable ? this._makeCancellable() : null;
        const opts = {
            wsUrl: parsed.wsUrl,
            httpUrl: parsed.httpUrl,
            cancellable: this._cancellable,
            runtime: this._runtime,
            onOpen: () => this._onOpen(epoch),
            onMessage: (text) => this._onMessage(epoch, text),
            onClose: (err) => this._onClose(epoch, err),
        };

        let handle;
        try {
            handle = this._transportFactory(opts);
        } catch (e) {
            this._onClose(epoch, String((e && e.message) || e));
            return;
        }
        if (handle && typeof handle.then === 'function') {
            handle.then((h) => {
                if (!this._running || epoch !== this._epoch) {
                    try {
                        if (h)
                            h.close();
                    } catch (error) {
                        console.error(`MV7+ late transport cleanup failed: ${error.message}`);
                    }
                    return;
                }
                this._transport = h;
            }).catch((e) => this._onClose(epoch, String((e && e.message) || e)));
        } else {
            this._transport = handle;
        }
    }

    _onOpen(epoch) {
        if (!this._running || epoch !== this._epoch)
            return;
        this._socketOpen = true;
        this._meterSupported = false;
        this._meterSubscribed = false;
        this._dispatch({ type: 'socket-open' });
        this._armWatchdog(epoch);
    }

    _onMessage(epoch, text) {
        if (!this._running || epoch !== this._epoch)
            return;
        const ev = parseServerMessage(text);

        switch (ev.kind) {
        case 'meter':
            if (!this._onMeter || !this._meterEnabled || !this._meterSubscribed ||
                this._conn.connection !== Connection.CONNECTED)
                return;
            if (this._meterTimerId)
                this._clock.clearTimeout(this._meterTimerId);
            this._meterTimerId = 0;
            this._onMeter(ev.meter);
            if (ev.meter.available) {
                this._meterTimerId = this._clock.setTimeout(() => {
                    this._meterTimerId = 0;
                    this._clearMeter('No fresh input-level reading');
                }, 2000);
            }
            return;
        case 'invalid':
            this._dispatch(ev);
            this._onError?.(ev.error);
            return;
        case 'error':
            // Command/protocol error: surface it, but do not tear down a
            // healthy connection.
            this._dispatch(ev);
            if (this._onError)
                this._onError(ev.error);
            return;
        case 'state':
            this._dispatch(ev);
            if (ev.normalized && ev.normalized.valid) {
                this._attempt = 0; // healthy: reset backoff
                this._armWatchdog(epoch); // freshness reset
                if (this._onMeter && this._meterEnabled && !this._meterSupported)
                    this._clearMeter('Update mv7web to enable live input metering');
            }
            return;
        case 'status':
            this._dispatch(ev);
            this._meterSupported = ev.meterSupported;
            this._syncMeterSubscription();
            // Availability heartbeats cannot extend the lifetime of old mute data.
            return;
        default:
            // invalid / unknown / future frame: ignore, do not disturb state.
            return;
        }
    }

    _onClose(epoch, err) {
        if (!this._running || epoch !== this._epoch)
            return;
        this._socketOpen = false;
        this._clearWatchdog();
        this._closeTransport();
        this._cancel();
        this._dispatch({ type: 'socket-closed', error: err || null });
        this._epoch++; // invalidate any further callbacks from the dead socket
        this._scheduleReconnect();
    }

    _onWatchdog(epoch) {
        if (!this._running || epoch !== this._epoch)
            return;
        // Silent daemon: force the indicator down and reconnect.
        this._dispatch({ type: 'watchdog' });
        this._socketOpen = false;
        this._closeTransport();
        this._cancel();
        this._epoch++;
        this._scheduleReconnect();
    }

    _scheduleReconnect() {
        if (!this._running)
            return;
        this._clearReconnect();
        const delay = backoffDelay(this._attempt++, this._backoff);
        this._reconnectId = this._clock.setTimeout(() => {
            this._reconnectId = 0;
            if (!this._running)
                return;
            this._epoch++;
            this._connect(this._epoch);
        }, delay);
    }

    // --- timers ------------------------------------------------------------

    _armWatchdog(epoch) {
        this._clearWatchdog();
        if (!(this._watchdogMs > 0))
            return;
        this._watchdogId = this._clock.setTimeout(() => {
            this._watchdogId = 0;
            this._onWatchdog(epoch);
        }, this._watchdogMs);
    }

    _clearWatchdog() {
        if (this._watchdogId) {
            this._clock.clearTimeout(this._watchdogId);
            this._watchdogId = 0;
        }
    }

    _clearReconnect() {
        if (this._reconnectId) {
            this._clock.clearTimeout(this._reconnectId);
            this._reconnectId = 0;
        }
    }

    _clearTimers() {
        this._clearWatchdog();
        this._clearReconnect();
        this._clearMeter();
    }

    // --- teardown helpers --------------------------------------------------

    _closeTransport() {
        if (this._transport) {
            try {
                this._transport.close();
            } catch (error) {
                console.error(`MV7+ transport cleanup failed: ${error.message}`);
            }
            this._transport = null;
        }
    }

    _cancel() {
        if (this._cancellable) {
            try {
                this._cancellable.cancel();
            } catch (error) {
                console.error(`MV7+ cancellation failed: ${error.message}`);
            }
            this._cancellable = null;
        }
    }

    _fail(message) {
        this._clearTimers();
        this._cancel();
        this._closeTransport();
        this._socketOpen = false;
        this._conn = { connection: Connection.ERROR, state: null, error: message };
        this._emitUpdate();
        if (this._onError)
            this._onError(message);
    }

    // --- state dispatch ----------------------------------------------------

    _dispatch(event) {
        const next = nextConnectionState(this._conn, event);
        const changed = next.connection !== this._conn.connection ||
            next.state !== this._conn.state ||
            next.error !== this._conn.error;
        this._conn = next;
        if (changed)
            this._emitUpdate();
    }

    _emitUpdate() {
        if (this._conn.connection !== Connection.CONNECTED)
            this._clearMeter(this._meterEnabled ? 'Microphone unavailable' : 'Live input meter disabled');
        if (!this._onUpdate)
            return;
        this._onUpdate({
            connection: this._conn.connection,
            state: this._conn.state,
            error: this._conn.error,
        });
    }
}

export default DaemonClient;
