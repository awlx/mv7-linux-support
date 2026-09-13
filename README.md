# Shure MV7+ Linux Control

Control a **Shure MV7+** from a native GNOME app, a GNOME Shell panel extension,
or an optional web interface. All three use the same local Go daemon, `mv7web`.

Supports the MV7+ (`14ed:1019`), **not the original MV7**. This is an unofficial
community project, not affiliated with or supported by Shure Incorporated.
Shure and MV7+ are trademarks of Shure Incorporated.

## Features

- Gain in 0.5 dB steps, gain lock, hardware mute, and Auto Level.
- Filters, compressor, limiter, denoiser, Popper Stopper, tone, monitor mix,
  reverb, LED settings, and factory reset.
- A panel microphone icon that fills with input level and shows hardware mute.
  Numeric panel levels are optional; the menu and native app show peak/RMS dBFS.

![MV7+ web interface](docs/controls.png)

## Requirements

- Linux and a Shure MV7+ connected over USB.
- Native app: GJS, GTK 4.10+, libadwaita 1.4+, and libsoup 3
  (Ubuntu 24.04+, Debian 13+, or a recent Fedora).
- Panel extension: GNOME Shell 45-50. The native app also works without Shell.
- Live metering: PulseAudio or PipeWire's PulseAudio service, plus
  `pulseaudio-utils`.
- Building from source: Go 1.26.6 or newer.

## Install

The RPM and DEB include the daemon, native app, Shell extension, udev rule,
and optional **user** service. Installation does **not** enable the service or
extension. Use `dnf` or `apt` with recommended dependencies enabled to install
the desktop and metering dependencies too.

To build from a source checkout:

```bash
git clone https://github.com/awlx/mv7-linux-support.git
cd mv7-linux-support
```

### Fedora

```bash
sudo dnf install golang rpm-build desktop-file-utils systemd-rpm-macros glib2
./packaging/fedora/build-rpm.sh
sudo dnf install ./dist/RPMS/shure-mv7-0.3.2-1.*.rpm
```

### Ubuntu / Debian

```bash
sudo apt install golang-go dpkg-dev libglib2.0-bin
./packaging/ubuntu/build-deb.sh
sudo apt install ./dist/DEBS/shure-mv7_0.3.2-1_*.deb
```

If you already have a built package, only the final install command is needed.
Container build recipes are available for [Fedora](packaging/fedora/Containerfile)
and [Ubuntu/Debian](packaging/ubuntu/Containerfile).

### Enable the app and panel

Reconnect the microphone after installation, then start the daemon as your
desktop user:

```bash
systemctl --user daemon-reload
systemctl --user enable --now mv7web.service
```

Log out and back in to load the newly installed extension, then enable it:

```bash
gnome-extensions enable shure-mv7plus@shure-mv7.local
```

Open **MV7+ Control** from the application menu, or run `mv7-native`.
No separate extension download or source installation is required.
The service retries if the microphone is absent at login.

For the web interface, open <http://127.0.0.1:8090> or launch **Shure MV7+ Console**.
The web shortcut (`mv7web -open`) reuses a daemon at the configured address;
if the address is free, it starts one. It does not stop or restart the user service.

### Upgrade

Close **MV7+ Control** and disable the extension before replacing its files:

```bash
gnome-extensions disable shure-mv7plus@shure-mv7.local
```

Update your checkout with `git pull --ff-only`, then rebuild and install the
package using the commands above. Restart the installed daemon:

```bash
systemctl --user daemon-reload
systemctl --user restart mv7web.service
```

Log out and back in, re-enable the extension, and reopen the app. Source-only
users should repeat the source installation below and restart their foreground
daemon instead of using these service commands.

### Install from source without a package

From the same checkout, install the desktop dependencies:

```bash
# Fedora
sudo dnf install gjs gtk4 libadwaita libsoup3 glib2 pulseaudio-utils

# Ubuntu / Debian
sudo apt install gjs gir1.2-gtk-4.0 gir1.2-adw-1 gir1.2-soup-3.0 libglib2.0-bin pulseaudio-utils
```

Build the daemon, install the HID permission rule, and install the desktop clients:

```bash
mkdir -p bin
go build -trimpath -o bin/mv7web ./cmd/mv7web
sudo install -Dm644 packaging/fedora/62-shure-mv7plus.rules \
  /etc/udev/rules.d/62-shure-mv7plus.rules
sudo udevadm control --reload-rules
bash packaging/gnome/install.sh
```

Reconnect the microphone and log out/back in. Start `./bin/mv7web` in a terminal,
then open **MV7+ Control** and enable the extension as above. The desktop launcher
is also available at `~/.local/bin/mv7-native`.

`packaging/gnome/install.sh` installs only the per-user desktop clients and honors
`XDG_DATA_HOME`. It does **not** install `mv7web` on your `PATH` or install a systemd service.
The supplied udev rule grants an active local desktop user HID access;
headless or SSH-only sessions need separately configured device permissions.

## Using the controls

The app exposes all microphone settings; the panel menu provides quick controls.
Manual gain is disabled while Auto Level or gain lock is enabled.
Hardware changes appear automatically.

The panel distinguishes live, muted, disconnected, and unknown/error states.
Its mute indicator reflects the **microphone's hardware mute**, not system or
per-application mute. It does not guarantee that another application is receiving
audio.

Open extension preferences with:

```bash
gnome-extensions prefs shure-mv7plus@shure-mv7.local
```

Enable **Show level numbers in panel** to add numeric dBFS beside the filling
icon. It is off by default. The app's Connection page and extension preferences
also let you change the **Daemon URL** if you use a different local port.

### Live input meter

The meter shows captured input level in **dBFS**, not configured gain or dBm.
The menu and app show sample peak and RMS.
Bars cover -60 to 0 dBFS, while numeric readings have a -90 dBFS floor.
System input gain/mute and microphone processing can affect these readings.

The desktop meter fades from green below -18 dBFS through yellow near -6 dBFS
to red near full scale. Its separate peak marker holds for one second, then
falls at 12 dB per second; the bar and Peak/RMS details remain live.
**CLIPPING** indicates actual full-scale samples, not just a high level.

The daemon selects a single MV7+ USB audio source rather than intentionally
using the default microphone. Missing or ambiguous sources show `-- dBFS`;
hardware controls still work. Routing is checked periodically, so changes
between checks cannot be ruled out.

The meter measures the direct MV7+ source, not the EasyEffects output.
On PipeWire/WirePlumber it opts out of automatic source moves; other applications
can still use EasyEffects.

**Privacy:** audio is processed in memory, never saved or sent to the clients;
only level numbers are transmitted. Capture runs while the microphone is
available and at least one connected client has **Live input meter** enabled.
Your desktop may show a microphone-in-use indicator. Disable metering in both
clients, or close the app and disable the extension, to stop capture.

### Remote control

The default endpoint is `127.0.0.1:8090`. The daemon has **no authentication or
TLS**; keep it on loopback and use an SSH tunnel for remote access:

```bash
ssh -N -L 8090:127.0.0.1:8090 user@microphone-pc
```

Connect the app, extension, or browser to the local forwarded address.
The GNOME clients accept only loopback HTTP/HTTPS URLs.

## Troubleshooting

| Problem | Check |
|---|---|
| Microphone not found | Confirm `lsusb` shows `14ed:1019`. Reconnect after installing the udev rule and run as your desktop user, not root. |
| App or panel cannot connect | Check `systemctl --user status mv7web.service`, the configured Daemon URL, and whether another process is using port 8090. |
| Meter unavailable | Install `pulseaudio-utils`; ensure PulseAudio or `pipewire-pulse` runs for the same user as the daemon and exposes one MV7+ input. |
| Controls revert | Check daemon logs for HID errors. Gain also requires Auto Level and gain lock to be off. |

Read service logs with:

```bash
journalctl --user -u mv7web.service -f
```

## Disable or remove

```bash
gnome-extensions disable shure-mv7plus@shure-mv7.local
systemctl --user disable --now mv7web.service  # Packaged service only
```

Close the native app as well. To remove a packaged installation, use
`sudo dnf remove shure-mv7` or `sudo apt remove shure-mv7`.

## Development

```bash
go test -race ./...
go vet ./...
node --test gnome-extension/tests/*.test.js gnome-app/tests/*.test.mjs internal/webui/app.test.cjs
bash packaging/gnome/test-install.sh  # Linux
```

On a Linux desktop with the native dependencies installed:

```bash
glib-compile-schemas --strict gnome-app/schemas
gjs -m gnome-app/main.js
```

`gjs -m gnome-app/tests/widgets.gjs` checks GTK widgets with a mocked connection;
`gjs -m gnome-extension/tests/live.gjs` checks a loopback WebSocket fixture.
Neither replaces testing with a real microphone.

The web assets in `internal/webui/web` are embedded in the daemon binary.
Protocol code is in `internal/mv7`; capture and routing details are documented
in [`internal/meter/doc.go`](internal/meter/doc.go).

## License and credits

Copyright (C) 2026 Annika Wickert. Licensed under **GPL-3.0-only**; see [LICENSE](LICENSE).
The binary MV7+ protocol implementation is based on the GPL-3.0-licensed captures,
documentation, and implementation in [Humblemonk/shurectl](https://github.com/Humblemonk/shurectl).
