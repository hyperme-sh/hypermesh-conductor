#!/usr/bin/env bash
# Build static Conductor binaries and pack .deb and .rpm for Linux.
# Filenames have no version, matching the Visor bundle. The package version is
# <VERSION>~ci.<sha>. A tag does not change that until a release is authorized.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if ! command -v nfpm >/dev/null 2>&1; then
  echo "nfpm is required" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 1
fi

base_version=$(tr -d '[:space:]' < VERSION)
if [[ -z "$base_version" ]]; then
  echo "VERSION is empty" >&2
  exit 1
fi

if [[ -n "${GITHUB_SHA:-}" ]]; then
  sha="${GITHUB_SHA:0:12}"
else
  sha=$(git rev-parse --short=12 HEAD)
fi
version="${base_version}~ci.${sha}"

if [[ "$version" == *[/[:space:]]* ]]; then
  echo "refusing version ${version}" >&2
  exit 1
fi

targets=(amd64 arm64)
if [[ $# -gt 0 ]]; then
  targets=("$@")
fi

mkdir -p dist
find dist -mindepth 1 -delete

for goarch in "${targets[@]}"; do
  case "$goarch" in
    amd64)
      deb_arch=amd64
      rpm_arch=x86_64
      asset_arch=x86_64
      elf_needle="x86-64"
      ;;
    arm64)
      deb_arch=arm64
      rpm_arch=aarch64
      asset_arch=aarch64
      elf_needle="ARM aarch64"
      ;;
    *)
      echo "unsupported arch $goarch" >&2
      exit 1
      ;;
  esac

  raw="dist/hypermesh-conductor-${asset_arch}"
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$raw" ./cmd/conductor
  chmod 0755 "$raw"
  desc=$(file -b "$raw")
  if ! grep -q "$elf_needle" <<<"$desc" || ! grep -q 'statically linked' <<<"$desc"; then
    echo "$raw is not a static $elf_needle ELF" >&2
    echo "$desc" >&2
    exit 1
  fi

  deb="dist/hypermesh-conductor-${asset_arch}.deb"
  rpm="dist/hypermesh-conductor-${asset_arch}.rpm"
  CONDUCTOR_BIN="$raw" VERSION="$version" NFPM_ARCH="$deb_arch" \
    nfpm package --config packaging/nfpm.yaml --packager deb --target "$deb"
  CONDUCTOR_BIN="$raw" VERSION="$version" NFPM_ARCH="$deb_arch" \
    nfpm package --config packaging/nfpm.yaml --packager rpm --target "$rpm"

  got_deb_arch=$(dpkg-deb -f "$deb" Architecture)
  got_deb_name=$(dpkg-deb -f "$deb" Package)
  got_deb_home=$(dpkg-deb -f "$deb" Homepage)
  if [[ "$got_deb_arch" != "$deb_arch" || "$got_deb_name" != "hypermesh-conductor" || "$got_deb_home" != "https://github.com/hyperme-sh/hypermesh-conductor" ]]; then
    echo "unexpected deb metadata for $deb: $got_deb_name $got_deb_arch $got_deb_home" >&2
    exit 1
  fi
  if ! dpkg-deb -c "$deb" | grep -q ' ./usr/bin/conductor$'; then
    echo "$deb does not install /usr/bin/conductor" >&2
    exit 1
  fi
  if command -v rpm >/dev/null 2>&1; then
    got_rpm=$(rpm -qp --queryformat '%{NAME} %{ARCH} %{URL}' "$rpm")
    if [[ "$got_rpm" != "hypermesh-conductor ${rpm_arch} https://github.com/hyperme-sh/hypermesh-conductor" ]]; then
      echo "unexpected rpm metadata for $rpm: $got_rpm" >&2
      exit 1
    fi
  fi
done

(
  cd dist
  mapfile -t files < <(find . -maxdepth 1 -type f ! -name checksums.txt -printf '%P\n' | sort)
  sha256sum -- "${files[@]}" > checksums.txt
)

echo "packed ${version} into dist/"
ls -1 dist
