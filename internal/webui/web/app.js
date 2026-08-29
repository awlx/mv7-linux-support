// MV7+ Console — WebSocket client and UI bindings.
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  let ws = null;
  let state = null;
  let reconnectTimer = null;

  const connLabel = $("connlabel");
  const dot = $("dot");

  // ── WebSocket ─────────────────────────────────────────────
  function connect() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${proto}://${location.host}/ws`);

    ws.onopen = () => {
      setConn("connected", true);
      send({ action: "get_state" });
    };

    ws.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      if (msg.type === "state") applyState(msg.state);
      else if (msg.type === "error") toast(msg.error, true);
    };

    ws.onclose = () => {
      setConn("disconnected", false);
      scheduleReconnect();
    };

    ws.onerror = () => { ws.close(); };
  }

  function scheduleReconnect() {
    if (reconnectTimer) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, 3000);
  }

  function send(msg) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg));
    }
  }

  function setConn(label, ok) {
    connLabel.textContent = label;
    dot.className = "dot" + (ok ? " on" : " err");
  }

  // ── State application ────────────────────────────────────
  function applyState(s) {
    state = s;

    // Header
    $("fw").textContent = s.firmware && s.firmware !== "Unknown" ? `fw ${s.firmware}` : "";
    $("serial").textContent = s.serial && s.serial !== "Unknown" ? s.serial : "";

    // Mute
    $("mutebtn").classList.toggle("muted", s.muted);
    $("mutelabel").textContent = s.muted ? "Unmute" : "Mute";

    // Mode
    document.querySelectorAll("#modeseg .seg-btn").forEach((b) => {
      b.classList.toggle("active", Number(b.dataset.mode) === (s.auto_level ? 1 : 0));
    });

    // Gain
    setRange("gain", s.gain_db);
    if (document.activeElement !== $("gaininput")) $("gaininput").value = s.gain_db;
    setCheck("gainlocked", s.gain_locked);
    $("gain").disabled = s.gain_locked;
    $("gaininput").disabled = s.gain_locked;

    // DSP
    setSelect("hpf", s.hpf);
    setCheck("limiter", s.limiter);
    setSelect("compressor", s.compressor);
    setCheck("denoiser", s.denoiser);
    setCheck("popper", s.popper_stopper);
    setCheck("mutebtnactive", s.mute_btn_active);

    // Tone
    setRange("tone", s.tone);
    $("toneval").textContent = toneLabel(s.tone);

    // Mix
    setRange("micmix", s.mic_mix);
    $("micmixval").textContent = `${s.mic_mix}%`;
    setRange("playmix", s.playback_mix);
    $("playmixval").textContent = `${s.playback_mix}%`;

    // Reverb
    setCheck("reverbout", s.reverb_output);
    setCheck("reverbmon", s.reverb_monitor);
    setSelect("reverbtype", s.reverb_type);
    setRange("reverbint", s.reverb_intensity);
    $("reverbintval").textContent = `${s.reverb_intensity}%`;

    // LED
    setSelect("ledbehavior", s.led_behavior);
    setSelect("ledbrightness", s.led_brightness);
    setSelect("ledlivetheme", s.led_live_theme);
    setSelect("ledsolidtheme", s.led_solid_theme);
    setSelect("ledpulsingtheme", s.led_pulsing_theme);
    setColor("ledsolidcolor", s.led_solid_color);
    setColor("ledpulsingcolor", s.led_pulsing_color);
    setColor("ledliveedge", s.led_live_edge);
    setColor("ledlivemiddle", s.led_live_middle);
    setColor("ledliveinterior", s.led_live_interior);
  }

  function toneLabel(t) {
    if (t < -30) return "Dark";
    if (t > 30) return "Bright";
    return "Natural";
  }

  function setRange(id, val) {
    const el = $(id);
    if (el && Number(el.value) !== Number(val)) el.value = val;
    el.style.setProperty("--fill", `${(val / (el.max - el.min)) * 100}%`);
  }

  function setSelect(id, val) {
    const el = $(id);
    if (el && Number(el.value) !== Number(val)) el.value = String(val);
  }

  function setCheck(id, val) {
    const el = $(id);
    if (el && el.checked !== !!val) el.checked = !!val;
  }

  function setColor(id, rgb) {
    const el = $(id);
    if (!el || !rgb) return;
    const hex = rgbToHex(rgb[0], rgb[1], rgb[2]);
    if (el.value !== hex) el.value = hex;
  }

  function rgbToHex(r, g, b) {
    return "#" + [r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("");
  }

  // ── Control wiring ────────────────────────────────────────
  function wire(id, action, transform) {
    const el = $(id);
    if (!el) return;
    el.addEventListener("change", () => {
      if (!state) return;
      if (!el.checkValidity()) {
        el.reportValidity();
        return;
      }
      send({ action, params: { value: transform(el) } });
    });
  }

  wire("gain", "set_gain", (el) => Number(el.value));
  wire("gaininput", "set_gain", (el) => Number(el.value));
  $("gain").addEventListener("input", () => { $("gaininput").value = $("gain").value; });
  $("gaininput").addEventListener("input", () => {
    if ($("gaininput").checkValidity()) setRange("gain", Number($("gaininput").value));
  });
  wire("gainlocked", "set_gain_locked", (el) => el.checked);
  wire("hpf", "set_hpf", (el) => Number(el.value));
  wire("limiter", "set_limiter", (el) => el.checked);
  wire("compressor", "set_compressor", (el) => Number(el.value));
  wire("denoiser", "set_denoiser", (el) => el.checked);
  wire("popper", "set_popper_stopper", (el) => el.checked);
  wire("mutebtnactive", "set_mute_btn", (el) => el.checked);
  wire("tone", "set_tone", (el) => Number(el.value));
  wire("micmix", "set_mic_mix", (el) => Number(el.value));
  wire("playmix", "set_playback_mix", (el) => Number(el.value));
  wire("reverbout", "set_reverb_output", (el) => el.checked);
  wire("reverbmon", "set_reverb_monitor", (el) => el.checked);
  wire("reverbtype", "set_reverb_type", (el) => Number(el.value));
  wire("reverbint", "set_reverb_intensity", (el) => Number(el.value));
  wire("ledbehavior", "set_led_behavior", (el) => Number(el.value));
  wire("ledbrightness", "set_led_brightness", (el) => Number(el.value));
  wire("ledlivetheme", "set_led_live_theme", (el) => Number(el.value));
  wire("ledsolidtheme", "set_led_solid_theme", (el) => Number(el.value));
  wire("ledpulsingtheme", "set_led_pulsing_theme", (el) => Number(el.value));

  function wireColor(id, action) {
    const el = $(id);
    if (!el) return;
    el.addEventListener("change", () => {
      if (!state) return;
      const hex = el.value.replace("#", "");
      const rgb = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16));
      send({ action, params: { rgb } });
    });
  }

  wireColor("ledsolidcolor", "set_led_solid_color");
  wireColor("ledpulsingcolor", "set_led_pulsing_color");
  wireColor("ledliveedge", "set_led_live_edge");
  wireColor("ledlivemiddle", "set_led_live_middle");
  wireColor("ledliveinterior", "set_led_live_interior");

  $("mutebtn").addEventListener("click", () => {
    send({ action: "set_mute", params: { value: !(state && state.muted) } });
  });

  document.querySelectorAll("#modeseg .seg-btn").forEach((b) => {
    b.addEventListener("click", () => {
      send({ action: "set_auto_level", params: { value: Number(b.dataset.mode) === 1 } });
    });
  });

  $("factoryreset").addEventListener("click", () => {
    if (confirm("Reset the MV7+ to factory defaults? The device will reconnect.")) {
      send({ action: "factory_reset" });
      toast("Factory reset sent — device is reconnecting…");
    }
  });

  // ── Toast ────────────────────────────────────────────────
  let toastTimer = null;
  function toast(text, isErr) {
    const t = $("toast");
    t.textContent = text;
    t.classList.toggle("err", !!isErr);
    t.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove("show"), 3000);
  }

  connect();
})();
