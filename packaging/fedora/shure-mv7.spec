Name:           shure-mv7
Version:        0.1.8
Release:        1%{?dist}
Summary:        Unofficial Linux web console for the Shure MV7+ microphone
%global debug_package %{nil}

License:        GPL-3.0-only
URL:            https://github.com/awlx/mv7-linux-support
Source0:        %{url}/archive/v%{version}.tar.gz#/%{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.26
BuildRequires:  desktop-file-utils
BuildRequires:  systemd-rpm-macros
Requires:       systemd-udev
Requires:       xdg-utils
Requires:       procps-ng

%description
Shure MV7+ Web Console is an unofficial independent Linux application that
controls a Shure MV7+ microphone through its vendor HID interface. It serves
an embedded local web interface for gain, DSP, monitoring, reverb, LED, and
mute controls. It is not affiliated with, endorsed by, or supported by Shure
Incorporated.

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

%changelog
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