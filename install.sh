#!/bin/sh
# install.sh — build + install the projx-engine CLI on macOS / Linux.
# Puts the binary on ~/.local/bin. Run from the repo:  ./install.sh
set -e
cd "$(dirname "$0")"

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
GOWORK=off go build -o "$BIN/projx-engine" .
echo "installed -> $BIN/projx-engine"

case ":$PATH:" in
  *":$BIN:"*) echo "$BIN already on PATH." ;;
  *) echo "add $BIN to your PATH (e.g. echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.profile)" ;;
esac
echo "done. In your repo:  projx-engine init"
