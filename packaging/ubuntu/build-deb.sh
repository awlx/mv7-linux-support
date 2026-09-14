#!/usr/bin/env bash
set -euo pipefail

name="shure-mv7"
version="0.3.3"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work_dir="$(mktemp -d)"
package_root="${work_dir}/${name}"
target_arch="${DEB_TARGET_ARCH:-$(dpkg --print-architecture)}"

cleanup() {
  rm -rf "${work_dir}"
}
trap cleanup EXIT

case "${target_arch}" in
  amd64) goarch="amd64" ;;
  arm64) goarch="arm64" ;;
  *)
    echo "unsupported DEB_TARGET_ARCH: ${target_arch}" >&2
    exit 1
    ;;
esac

for command in go dpkg-deb gzip glib-compile-schemas; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "missing required command: ${command}" >&2
    exit 1
  fi
done

mkdir -p \
  "${package_root}/DEBIAN" \
  "${package_root}/usr/bin" \
  "${package_root}/usr/lib/udev/rules.d" \
  "${package_root}/usr/share/applications" \
  "${package_root}/usr/share/doc/${name}" \
  "${package_root}/usr/share/lintian/overrides" \
  "${package_root}/usr/share/man/man1" \
  "${repo_root}/dist/DEBS"

CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
  go build -trimpath -ldflags="-s -w" \
  -o "${package_root}/usr/bin/mv7web" "${repo_root}/cmd/mv7web"

install -m0644 "${repo_root}/packaging/fedora/shure-mv7.desktop" \
  "${package_root}/usr/share/applications/shure-mv7.desktop"
install -m0644 "${repo_root}/packaging/fedora/62-shure-mv7plus.rules" \
  "${package_root}/usr/lib/udev/rules.d/62-shure-mv7plus.rules"
gzip -9cn "${repo_root}/packaging/fedora/mv7web.1" \
  > "${package_root}/usr/share/man/man1/mv7web.1.gz"
install -m0644 "${repo_root}/README.md" \
  "${package_root}/usr/share/doc/${name}/README.md"
install -m0644 "${repo_root}/LICENSE" \
  "${package_root}/usr/share/doc/${name}/copyright"
gzip -9cn "${repo_root}/packaging/ubuntu/changelog" \
  > "${package_root}/usr/share/doc/${name}/changelog.Debian.gz"
install -m0644 "${repo_root}/packaging/ubuntu/lintian-overrides" \
  "${package_root}/usr/share/lintian/overrides/${name}"

sed \
  -e "s/@VERSION@/${version}/g" \
  -e "s/@ARCH@/${target_arch}/g" \
  "${repo_root}/packaging/ubuntu/control.in" \
  > "${package_root}/DEBIAN/control"
printf '\n' >> "${package_root}/DEBIAN/control"
install -m0755 "${repo_root}/packaging/ubuntu/postinst" \
  "${package_root}/DEBIAN/postinst"
install -m0755 "${repo_root}/packaging/ubuntu/postrm" \
  "${package_root}/DEBIAN/postrm"

bash "${repo_root}/packaging/gnome/stage.sh" "${package_root}" /usr

output="${repo_root}/dist/DEBS/${name}_${version}-1_${target_arch}.deb"
dpkg-deb --root-owner-group --build "${package_root}" "${output}"
echo "built ${output}"