#!/usr/bin/env bash
set -euo pipefail

if [[ "${MV7_SHELL_LAYOUT_TEST:-}" != 1 ]]; then
  echo "Set MV7_SHELL_LAYOUT_TEST=1 inside a disposable Linux environment with GNOME Shell."
  exit 1
fi
for command in gnome-shell dbus-run-session gsettings glib-compile-schemas timeout; do
  command -v "${command}" >/dev/null || { echo "Missing required command: ${command}" >&2; exit 1; }
done

source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
export HOME="${temporary}/home"
export XDG_RUNTIME_DIR="${temporary}/runtime"
export XDG_DATA_HOME="${HOME}/.local/share"
export XDG_CONFIG_HOME="${HOME}/.config"
export XDG_CACHE_HOME="${HOME}/.cache"
export MV7_LAYOUT_RESULT="${temporary}/result.json"
export LIBGL_ALWAYS_SOFTWARE=1
unset DISPLAY WAYLAND_DISPLAY DBUS_SESSION_BUS_ADDRESS DBUS_SYSTEM_BUS_ADDRESS
mkdir -p "${XDG_RUNTIME_DIR}" "${XDG_CONFIG_HOME}" "${XDG_CACHE_HOME}"
chmod 700 "${XDG_RUNTIME_DIR}"
extension="${XDG_DATA_HOME}/gnome-shell/extensions/shure-mv7plus@shure-mv7.local"
mkdir -p "${extension}"
cp -R "${source_dir}/." "${extension}/"
mv "${extension}/extension.js" "${extension}/implementation.js"
cp "${extension}/tests/shell-fixture.js" "${extension}/extension.js"
glib-compile-schemas --strict "${extension}/schemas"

result=0
timeout 150s dbus-run-session -- bash -c '
  set -e
  export DBUS_SYSTEM_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS}"
  gsettings set org.gnome.shell enabled-extensions "[\"shure-mv7plus@shure-mv7.local\"]"
  gsettings set org.gnome.shell disable-user-extensions false
  gsettings set org.gnome.desktop.interface enable-animations false
  gsettings set org.gnome.desktop.session idle-delay 0
  gnome-shell --headless --wayland --no-x11 --virtual-monitor "${MV7_LAYOUT_MONITOR:-1280x900}"
' > "${temporary}/shell.log" 2>&1 || result=$?
if [[ -f "${MV7_LAYOUT_RESULT}" ]]; then
  cat "${MV7_LAYOUT_RESULT}"
  echo
fi
if [[ "${result}" != 0 ]] || ! grep -q '"passed": true' "${MV7_LAYOUT_RESULT}"; then
  tail -n 100 "${temporary}/shell.log" >&2
  exit 1
fi
if grep -E 'JS ERROR|allocation cycle|allocation loop' "${temporary}/shell.log"; then
  exit 1
fi
