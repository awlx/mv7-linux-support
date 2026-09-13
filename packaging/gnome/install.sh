#!/usr/bin/env bash
set -euo pipefail

if [[ $# > 0 ]]; then
  if [[ $# == 1 && "$1" == "--help" ]]; then
    echo "usage: $0"
    echo "Installs the native app and Shell extension for the current user."
    echo "Uses XDG_DATA_HOME (default: HOME/.local/share) and HOME/.local/bin."
    echo "Does not install/start mv7web or enable the Shell extension."
    exit 0
  fi
  echo "Unsupported argument; use --help for usage." >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"
bin_home="${HOME}/.local/bin"
if [[ "${data_home}" != /* || "${HOME}" != /* ]]; then
  echo "HOME and XDG_DATA_HOME must be absolute paths." >&2
  exit 1
fi
case "${data_home}${bin_home}" in
  *$'\n'*|*$'\r'*) echo "Installation paths cannot contain newlines." >&2; exit 1 ;;
esac

staging="$(mktemp -d)"
trap 'rm -rf -- "${staging}"' EXIT
bash "${repo_root}/packaging/gnome/stage.sh" "${staging}" /usr

uuid="shure-mv7plus@shure-mv7.local"
mkdir -p "${data_home}/gnome-shell/extensions/${uuid}" \
  "${data_home}/shure-mv7" "${data_home}/applications" \
  "${data_home}/icons/hicolor/scalable/apps" "${bin_home}"
# Copy only the staged, owned payload; preserve unrelated files on upgrades.
cp -R "${staging}/usr/share/gnome-shell/extensions/${uuid}/." \
  "${data_home}/gnome-shell/extensions/${uuid}/"
cp -R "${staging}/usr/share/shure-mv7/." "${data_home}/shure-mv7/"
install -m644 "${staging}/usr/share/icons/hicolor/scalable/apps/io.github.awlx.MV7.svg" \
  "${data_home}/icons/hicolor/scalable/apps/io.github.awlx.MV7.svg"
printf '#!/usr/bin/env bash\nexec gjs -m %q "$@"\n' \
  "${data_home}/shure-mv7/gnome-app/main.js" > "${bin_home}/mv7-native"
chmod 755 "${bin_home}/mv7-native"

launcher="${bin_home}/mv7-native"
launcher="${launcher//\\/\\\\}"
launcher="${launcher//\"/\\\"}"
launcher="${launcher//\$/\\\$}"
launcher="${launcher//\`/\\\`}"
launcher="${launcher//%/%%}"
while IFS= read -r line; do
  if [[ "${line}" == Exec=* ]]; then
    printf 'Exec="%s"\n' "${launcher}"
  else
    printf '%s\n' "${line}"
  fi
done < "${repo_root}/packaging/gnome/io.github.awlx.MV7.desktop" \
  > "${data_home}/applications/io.github.awlx.MV7.desktop"

echo "Installed MV7+ Control and the GNOME Shell extension."
echo "Start mv7web separately, then launch ${bin_home}/mv7-native."
echo "Log out and back in before enabling: gnome-extensions enable ${uuid}"
