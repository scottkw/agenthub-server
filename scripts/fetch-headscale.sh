#!/usr/bin/env bash
# Download a pinned Headscale binary for the host platform into ./bin/headscale.
# Usage: scripts/fetch-headscale.sh [VERSION]
set -euo pipefail

VERSION="${1:-0.28.0}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "unsupported arch: $ARCH_RAW" >&2; exit 1 ;;
esac

case "$OS" in
    linux|darwin|freebsd) ;;
    *) echo "unsupported os: $OS (Headscale publishes no Windows binary)" >&2; exit 1 ;;
esac

BIN_NAME="headscale_${VERSION}_${OS}_${ARCH}"
URL="https://github.com/juanfont/headscale/releases/download/v${VERSION}/${BIN_NAME}"
OUT_DIR="$(cd "$(dirname "$0")/.." && pwd)/bin"
OUT="$OUT_DIR/headscale"

mkdir -p "$OUT_DIR"

if [[ -x "$OUT" ]] && "$OUT" version 2>/dev/null | grep -q "$VERSION"; then
    echo "headscale $VERSION already present at $OUT"
    exit 0
fi

echo "fetching $URL"
curl -fsSL -o "$OUT.download" "$URL"
chmod +x "$OUT.download"
mv "$OUT.download" "$OUT"

echo "installed $("$OUT" version | head -n1) → $OUT"
