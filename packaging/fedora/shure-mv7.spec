Name:           shure-mv7
Version:        0.3.4
Release:        1%{?dist}
Summary:        Unofficial Linux web console for the Shure MV7+ microphone
%global debug_package %{nil}

License:        GPL-3.0-only
URL:            https://github.com/awlx/mv7-linux-support
Source0:        %{url}/archive/v%{version}.tar.gz#/%{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.26
BuildRequires:  desktop-file-utils
BuildRequires:  systemd-rpm-macros
BuildRequires:  glib2
Requires:       systemd-udev
Requires:       xdg-utils
Recommends:     gjs
Recommends:     gtk4 >= 4.10
Recommends:     libadwaita >= 1.4
Recommends:     libsoup3
Recommends:     pulseaudio-utils
Suggests:       gnome-shell >= 45

%description
Shure MV7+ Web Console is an unofficial independent Linux application that
controls a Shure MV7+ microphone through its vendor HID interface. It serves
an embedded local web interface for gain, DSP, monitoring, reverb, LED, and
mute controls. It is not affiliated with, endorsed by, or supported by Shure
Incorporated.
Includes a native GTK4/libadwaita application and a GNOME Shell panel
extension. Both connect to the same local mv7web daemon.

%prep
%autosetup

%build
export CGO_ENABLED=0
%ifarch x86_64
export GOARCH=amd64
%endif
%ifarch aarch64
export GOARCH=arm64
%endif
export GOFLAGS="-mod=vendor -buildmode=pie"
go build -trimpath -ldflags="-s -w" -o mv7web ./cmd/mv7web

%install
install -Dpm0755 mv7web %{buildroot}%{_bindir}/mv7web
desktop-file-install \
  --dir=%{buildroot}%{_datadir}/applications \
  packaging/fedora/shure-mv7.desktop
install -Dpm0644 packaging/fedora/62-shure-mv7plus.rules \
  %{buildroot}%{_udevrulesdir}/62-shure-mv7plus.rules
install -Dpm0644 packaging/fedora/mv7web.1 \
  %{buildroot}%{_mandir}/man1/mv7web.1
bash packaging/gnome/stage.sh %{buildroot} %{_prefix}

%check
export CGO_ENABLED=0
%ifarch x86_64
export GOARCH=amd64
%endif
%ifarch aarch64
export GOARCH=arm64
%endif
export GOFLAGS="-mod=vendor"
go test ./...

%files
%license LICENSE
%doc README.md
%{_bindir}/mv7web
%{_datadir}/applications/shure-mv7.desktop
%{_udevrulesdir}/62-shure-mv7plus.rules
%{_mandir}/man1/mv7web.1*
%{_bindir}/mv7-native
%{_datadir}/shure-mv7/
%{_datadir}/gnome-shell/extensions/shure-mv7plus@shure-mv7.local/
%{_datadir}/applications/io.github.awlx.MV7.desktop
%{_datadir}/icons/hicolor/scalable/apps/io.github.awlx.MV7.svg
%{_userunitdir}/mv7web.service

%post
%udev_post

%preun
%udev_preun

%changelog
* Mon Sep 14 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.3.4-1
- Put hardware mute above the live meter and gain controls in both GNOME clients
- Include full-size themed panel microphone icons and refreshed screenshots

* Mon Sep 14 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.3.3-1
- Keep Shell meter and panel allocations stable across changing readings
- Add native app and Shell screenshots to the documentation

* Sun Sep 13 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.3.2-1
- Add a live-input card with a decaying peak marker and headroom colour gradient

* Sun Sep 13 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.3.1-1
- Reuse the running daemon when opening the web console instead of terminating it
- Prevent automatic PipeWire/WirePlumber moves of the direct microphone meter
- Simplify installation instructions and remove the obsolete procps dependency

* Sun Sep 13 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.3.0-1
- Add shared MV7+ input metering, filling panel icon, and optional level numbers
- Recommend PulseAudio capture utilities for live dBFS

* Sun Sep 13 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.2.0-1
- Add native GNOME controls, live panel status, and an optional user service

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.8-1
- Support manual gain in 0.5 dB increments
- Add a synchronized numeric gain input

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.7-1
- Add MV7+ manual gain lock control and readback

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.6-1
- Ignore stale SET echoes while reading current feature state
- Match gain readback by GET response class, address, and prefix

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.5-1
- Always use the gain value read directly from the microphone
- Remove automatic host-side gain restoration
- Reject refreshes that do not include a current gain response

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.4-1
- Replace stale user-owned background processes on desktop launch
- Verify initial state discovery emits no gain SET packets

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.3-1
- Keep initial hardware discovery strictly read-only for gain
- Enable reconnect gain restoration only after an explicit gain command

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.2-1
- Send one gain command when the slider is released
- Keep periodic state reads from replacing the saved gain target

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.1-1
- Reconnect automatically after USB re-enumeration
- Preserve and restore gain without writing on initial discovery

* Fri Aug 28 2026 Annika Wickert <awlx@users.noreply.github.com> - 0.1.0-1
- Initial RPM release