#!/bin/sh
# install.sh — one-shot ProjX installer (no build from source).
#
# Downloads the prebuilt projx-engine binary for THIS OS/arch from the latest
# GitHub release of the public repo SirNiklas9/projx-engine, installs it to
# ~/.local/bin/projx-engine, marks it executable, then runs the GLOBAL bootstrap
# (merge the lifecycle hook into ~/.claude/settings.json preserving existing hooks,
# seed the global-scope floor, install the projx skill). Idempotent.
#
# It NEVER builds from source. If the latest release has no binary asset for this
# platform, it says so and exits — publish a release with prebuilt binaries first.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.sh | sh
#   # or, from a local checkout:  sh install.sh

set -eu

REPO="SirNiklas9/projx-engine"
BIN_DIR="${HOME}/.local/bin"
DEST="${BIN_DIR}/projx-engine"

# --- detect OS/arch -> conventional release asset name ---
os="$(uname -s)"
case "$os" in
  Linux)                            OS="linux" ;;
  Darwin)                           OS="darwin" ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT)  OS="windows" ;;
  *) echo "install: unsupported OS: $os" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)   ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *) echo "install: unsupported arch: $arch" >&2; exit 1 ;;
esac

ASSET="projx-engine_${OS}_${ARCH}"
[ "$OS" = "windows" ] && ASSET="${ASSET}.exe"

mkdir -p "$BIN_DIR"
echo "install: fetching ${ASSET} from ${REPO} (latest release)"

fetch_ok=0
# Prefer gh (auth + private-repo friendly); fall back to curl/wget of the public asset URL.
if command -v gh >/dev/null 2>&1; then
  if gh release download --repo "$REPO" --pattern "$ASSET" --output "$DEST" --clobber 2>/dev/null; then
    fetch_ok=1
  fi
fi

if [ "$fetch_ok" -eq 0 ]; then
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$URL" -o "$DEST" && fetch_ok=1 || true
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$URL" -O "$DEST" && fetch_ok=1 || true
  else
    echo "install: need gh, curl, or wget to download the binary" >&2
    exit 1
  fi
fi

if [ "$fetch_ok" -eq 0 ]; then
  rm -f "$DEST"
  echo "install: could not download ${ASSET}." >&2
  echo "install: the latest ${REPO} release has no binary for ${OS}/${ARCH} yet." >&2
  echo "install: publish a release with prebuilt binaries first (asset name: ${ASSET})." >&2
  echo "install: NOT falling back to building from source." >&2
  exit 1
fi

chmod +x "$DEST"
echo "install: installed -> ${DEST}"

case ":$PATH:" in
  *":$BIN_DIR:"*) : ;;
  *) echo "install: add $BIN_DIR to your PATH (e.g. echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.profile)" ;;
esac

# --- global bootstrap: hook (preserving existing) + global floor + projx skill ---
"$DEST" init --global
