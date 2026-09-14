#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "${temporary}"' EXIT
bash "${repo_root}/packaging/gnome/stage.sh" "${temporary}/package" /usr
extension="${temporary}/package/usr/share/gnome-shell/extensions/shure-mv7plus@shure-mv7.local"
test -s "${extension}/schemas/gschemas.compiled"
test -s "${extension}/panelWidgets.js"
test -s "${temporary}/package/usr/share/shure-mv7/gnome-app/schemas/gschemas.compiled"
test -x "${temporary}/package/usr/bin/mv7-native"
test -f "${temporary}/package/usr/lib/systemd/user/mv7web.service"
if find "${temporary}/package" -path '*/tests/*' | grep -q .; then
  echo "Test fixtures leaked into the installed payload." >&2
  exit 1
fi

export HOME="${temporary}/home with spaces"
export XDG_DATA_HOME="${HOME}/custom data"
mkdir -p "${HOME}"
bash "${repo_root}/packaging/gnome/install.sh"
installed="${XDG_DATA_HOME}/gnome-shell/extensions/shure-mv7plus@shure-mv7.local"
printf 'preserve\n' > "${installed}/user-note.txt"
bash "${repo_root}/packaging/gnome/install.sh"
test "$(cat "${installed}/user-note.txt")" = preserve
test -s "${installed}/schemas/gschemas.compiled"
test -s "${installed}/panelWidgets.js"
test -x "${HOME}/.local/bin/mv7-native"
bash -n "${HOME}/.local/bin/mv7-native"
grep -F "Exec=\"${HOME}/.local/bin/mv7-native\"" \
  "${XDG_DATA_HOME}/applications/io.github.awlx.MV7.desktop"
if command -v desktop-file-validate >/dev/null; then
  desktop-file-validate "${XDG_DATA_HOME}/applications/io.github.awlx.MV7.desktop"
fi
echo "GNOME installation checks passed."
