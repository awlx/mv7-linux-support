const assert = require("node:assert/strict");
const {readFileSync} = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function consoleHarness() {
  const elements = new Map();
  function element(id) {
    if (!elements.has(id)) {
      const classes = new Set();
      elements.set(id, {
        value: "0", min: 0, max: 100, disabled: false, textContent: "",
        classList: {
          add: (name) => classes.add(name),
          remove: (name) => classes.delete(name),
          toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name),
        },
        style: {setProperty() {}},
        handlers: {},
        addEventListener(name, callback) { this.handlers[name] = callback; },
        setAttribute(name, value) { this[name] = value; },
        checkValidity: () => true,
      });
    }
    return elements.get(id);
  }
  const html = readFileSync(path.join(__dirname, "web/index.html"), "utf8");
  const controls = [...html.matchAll(/<(?:button|input|select)[^>]*\bid="([^"]+)"/g)]
    .map((match) => element(match[1]));
  const sockets = [];
  class WebSocket {
    static OPEN = 1;
    constructor() {
      this.readyState = 1;
      this.sent = [];
      sockets.push(this);
    }
    send(data) { this.sent.push(JSON.parse(data)); }
    close() { this.onclose(); }
  }
  const context = {
    document: {
      getElementById: element,
      querySelectorAll: (selector) => selector.startsWith("main ") ? controls : [],
      activeElement: null,
    },
    WebSocket,
    location: {protocol: "http:", host: "127.0.0.1:8090"},
    setTimeout: () => 1,
    clearTimeout() {},
    confirm: () => true,
  };
  vm.runInNewContext(readFileSync(path.join(__dirname, "web/app.js"), "utf8"), context);
  return {
    element,
    socket: sockets[0],
    event: (message) => sockets[0].onmessage({data: JSON.stringify(message)}),
  };
}

function state(overrides = {}) {
  return {muted: true, gain_db: 12.5, gain_locked: false, auto_level: false, ...overrides};
}

test("socket open alone never confirms mute or enables controls", () => {
  const ui = consoleHarness();
  ui.socket.onopen();
  assert.equal(ui.element("mutebtn").disabled, true);
  assert.equal(ui.element("mutelabel").textContent, "Unknown");
  ui.event({type: "status", connected: true});
  assert.equal(ui.element("mutebtn").disabled, true);
});

test("hardware loss invalidates mute and blocks commands until fresh state", () => {
  const ui = consoleHarness();
  ui.event({type: "state", state: state()});
  assert.equal(ui.element("mutebtn").disabled, false);
  assert.equal(ui.element("mutelabel").textContent, "Unmute");
  ui.event({type: "status", connected: false, error: "HID disconnected"});
  assert.equal(ui.element("mutebtn").disabled, true);
  assert.equal(ui.element("mutelabel").textContent, "Unknown");
  assert.equal(ui.element("connlabel").title, "HID disconnected");
  ui.element("mutebtn").handlers.click();
  assert.equal(ui.socket.sent.length, 0);
  ui.event({type: "status", connected: true});
  assert.equal(ui.element("mutebtn").disabled, true);
  ui.event({type: "state", state: state({muted: false})});
  assert.equal(ui.element("mutebtn").disabled, false);
  assert.equal(ui.element("mutelabel").textContent, "Mute");
  ui.element("mutebtn").handlers.click();
  assert.equal(ui.socket.sent[0].action, "set_mute");
  assert.equal(ui.socket.sent[0].params.value, true);
});

test("malformed mute and closed socket do not leave confirmed state", () => {
  const ui = consoleHarness();
  ui.event({type: "state", state: state()});
  ui.event({type: "state", state: {}});
  assert.equal(ui.element("mutebtn").disabled, true);
  ui.event({type: "state", state: state()});
  ui.socket.onclose();
  assert.equal(ui.element("mutelabel").textContent, "Unknown");
  ui.socket.onmessage({data: "invalid json"});
  assert.equal(ui.element("mutebtn").disabled, true);
});

test("confirmed gain lock and Auto Level prevent manual gain changes", () => {
  const ui = consoleHarness();
  for (const overrides of [{gain_locked: true}, {auto_level: true}]) {
    ui.event({type: "state", state: state(overrides)});
    assert.equal(ui.element("gain").disabled, true);
    assert.equal(ui.element("gaininput").disabled, true);
  }
  ui.event({type: "state", state: state()});
  assert.equal(ui.element("gain").disabled, false);
});
