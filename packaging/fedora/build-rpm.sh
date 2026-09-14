#!/usr/bin/env bash
set -euo pipefail

version="0.3.3"
name="shure-mv7"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work_dir="$(mktemp -d)"
top_dir="${work_dir}/rpmbuild"
source_dir="${work_dir}/${name}-${version}"

cleanup() {
  rm -rf "${work_dir}"
}
trap cleanup EXIT

for command in go rpmbuild tar; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "missing required command: ${command}" >&2
    exit 1
  fi
done

mkdir -p "${source_dir}" "${top_dir}/SOURCES" \
  "${repo_root}/dist/RPMS" "${repo_root}/dist/SRPMS"
tar \
  --exclude=.git \
  --exclude=bin \
  --exclude=dist \
  --exclude=vendor \
  -C "${repo_root}" -cf - . | tar -C "${source_dir}" -xf -

(
  cd "${source_dir}"
  go mod vendor
)

tar -C "${work_dir}" -czf \
  "${top_dir}/SOURCES/${name}-${version}.tar.gz" \
  "${name}-${version}"

target_args=()
if [[ -n "${RPM_TARGET:-}" ]]; then
  target_args=(--target "${RPM_TARGET}")
fi

rpmbuild \
  --quiet \
  --define "_topdir ${top_dir}" \
  "${target_args[@]}" \
  -ba "${source_dir}/packaging/fedora/${name}.spec"

find "${top_dir}/RPMS" -type f -name '*.rpm' \
  -exec cp -v {} "${repo_root}/dist/RPMS/" \;
find "${top_dir}/SRPMS" -type f -name '*.rpm' \
  -exec cp -v {} "${repo_root}/dist/SRPMS/" \;