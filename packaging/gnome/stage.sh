#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || "$2" != /* ]]; then
  echo "usage: $0 DESTDIR /PREFIX" >&2
  exit 1
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
destination="$1"
prefix="${2%/}"
uuid="shure-mv7plus@shure-mv7.local"
extension="${destination}${prefix}/share/gnome-shell/extensions/${uuid}"
app="${destination}${prefix}/share/shure-mv7/gnome-app"
client="${destination}${prefix}/share/shure-mv7/gnome-extension"

command -v glib-compile-schemas >/dev/null || {
  echo "glib-compile-schemas is required to install GNOME settings." >&2
  exit 1
}

for file in metadata.json extension.js prefs.js stylesheet.css daemonClient.js state.js panelWidgets.js; do
  install -Dm644 "${repo_root}/gnome-extension/${file}" "${extension}/${file}"
done
install -Dm644 \
  "${repo_root}/gnome-extension/schemas/org.gnome.shell.extensions.shure-mv7plus.gschema.xml" \
  "${extension}/schemas/org.gnome.shell.extensions.shure-mv7plus.gschema.xml"
shopt -s nullglob
for icon in "${repo_root}"/gnome-extension/icons/*.svg; do
  install -Dm644 "${icon}" "${extension}/icons/$(basename "${icon}")"
done
glib-compile-schemas --strict "${extension}/schemas"

for file in main.js application.js controls.js; do
  install -Dm644 "${repo_root}/gnome-app/${file}" "${app}/${file}"
done
for file in daemonClient.js state.js; do
  install -Dm644 "${repo_root}/gnome-extension/${file}" "${client}/${file}"
done
install -Dm644 "${repo_root}/gnome-app/schemas/io.github.awlx.MV7.gschema.xml" \
  "${app}/schemas/io.github.awlx.MV7.gschema.xml"
glib-compile-schemas --strict "${app}/schemas"

install -Dm644 "${repo_root}/gnome-app/icons/io.github.awlx.MV7.svg" \
  "${destination}${prefix}/share/icons/hicolor/scalable/apps/io.github.awlx.MV7.svg"
install -Dm644 "${repo_root}/packaging/gnome/io.github.awlx.MV7.desktop" \
  "${destination}${prefix}/share/applications/io.github.awlx.MV7.desktop"
install -Dm644 "${repo_root}/packaging/gnome/mv7web.service" \
  "${destination}${prefix}/lib/systemd/user/mv7web.service"
mkdir -p "${destination}${prefix}/bin"
printf '#!/usr/bin/env bash\nexec gjs -m %q "$@"\n' \
  "${prefix}/share/shure-mv7/gnome-app/main.js" > "${destination}${prefix}/bin/mv7-native"
chmod 755 "${destination}${prefix}/bin/mv7-native"
test -s "${extension}/schemas/gschemas.compiled"
test -s "${app}/schemas/gschemas.compiled"
