#!/bin/sh
set -eu

root=${1:-"$(pwd)"}
version=${PROJX_VERSION:-latest}
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "ProjX has no published asset for architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux*) os=linux ;;
  darwin*) os=darwin ;;
  *) echo "ProjX has no Unix bootstrap asset for OS: $os" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
  release_base="https://github.com/SirNiklas9/projx-engine/releases/latest/download"
else
  release_base="https://github.com/SirNiklas9/projx-engine/releases/download/$version"
fi
temporary=$(mktemp -d "${TMPDIR:-/tmp}/projx-bootstrap.XXXXXX")
trap 'rm -rf "$temporary"' EXIT INT TERM
engine="$temporary/projx-engine"
asset="projx-engine_${os}_${arch}"
checksums="$temporary/projx-engine_checksums.txt"

if command -v curl >/dev/null 2>&1; then
  curl -fL "$release_base/$asset" -o "$engine"
  curl -fL "$release_base/projx-engine_checksums.txt" -o "$checksums"
elif command -v wget >/dev/null 2>&1; then
  wget -O "$engine" "$release_base/$asset"
  wget -O "$checksums" "$release_base/projx-engine_checksums.txt"
else
  echo "ProjX bootstrap requires curl or wget to fetch the public release." >&2
  exit 1
fi
expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$checksums")
[ -n "$expected" ] || { echo "ProjX checksum manifest does not contain $asset" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$engine" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$engine" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo "ProjX checksum verification failed for $asset" >&2; exit 1; }
chmod 700 "$engine"
"$engine" init --global --codex
"$engine" --root "$root" init --codex
"$engine" version
