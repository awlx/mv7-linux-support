// SPDX-License-Identifier: GPL-3.0-only
//
// Test doubles for exercising DaemonClient without GJS: a deterministic virtual
// clock and a controllable in-memory WebSocket transport.

/**
 * A virtual clock matching the {setTimeout, clearTimeout} shape the client
 * expects. Time only advances when tick() is called, so reconnect/backoff and
 * the freshness watchdog can be driven deterministically.
 */
export class FakeClock {
    constructor() {
        this._id = 1;
        this._timers = new Map();
        this.now = 0;
    }

    setTimeout(fn, ms) {
        const id = this._id++;
        this._timers.set(id, { fn, at: this.now + Math.max(0, ms) });
        return id;
    }

    clearTimeout(id) {
        this._timers.delete(id);
    }

    /** Advance virtual time by `ms`, firing due timers in chronological order. */
    tick(ms) {
        const target = this.now + ms;
        // Fire timers one at a time (a fired timer may schedule new ones).
        // Guard against runaway loops.
        for (let guard = 0; guard < 10000; guard++) {
            let nextId = null;
            let nextAt = Infinity;
            for (const [id, t] of this._timers) {
                if (t.at <= target && t.at < nextAt) {
                    nextAt = t.at;
                    nextId = id;
                }
            }
            if (nextId === null)
                break;
            const timer = this._timers.get(nextId);
            this._timers.delete(nextId);
            this.now = timer.at;
            timer.fn();
        }
        this.now = target;
    }

    pending() {
        return this._timers.size;
    }
}

/**
 * Build an injectable transport factory plus a registry of the transports it
 * created, so tests can drive open/message/close events and inspect sent
 * frames.
 */
export function makeFakeTransport() {
    const created = [];
    const factory = (opts) => {
        const transport = {
            opts,
            sent: [],
            closed: false,
            emitOpen() {
                opts.onOpen();
            },
            emitMessage(msg) {
                opts.onMessage(typeof msg === 'string' ? msg : JSON.stringify(msg));
            },
            emitClose(err) {
                opts.onClose(err == null ? null : err);
            },
            send(text) {
                this.sent.push(text);
                return true;
            },
            close() {
                this.closed = true;
            },
        };
        created.push(transport);
        return transport;
    };
    factory.created = created;
    factory.last = () => created[created.length - 1];
    return factory;
}

/** Attach a recorder to a DaemonClient-style options bag. */
export function makeRecorder() {
    const updates = [];
    const errors = [];
    return {
        updates,
        errors,
        onUpdate: (u) => updates.push(u),
        onError: (m) => errors.push(m),
        lastUpdate: () => updates[updates.length - 1],
        connections: () => updates.map((u) => u.connection),
    };
}
