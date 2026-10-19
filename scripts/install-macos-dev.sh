#!/usr/bin/env bash
# Install a FamClaw macOS build the developer way (issue #214).
#
# macOS darwin release binaries are ad-hoc-signed and NOT notarized by Apple
# (the project ships no Developer ID — see docs/MACOS.md), so a browser
# download is blocked by Gatekeeper. This script stays on the terminal path:
# it downloads the release archive for this Mac's architecture with curl (no
# quarantine attribute), verifies it against the goreleaser checksums.txt,
# clears any quarantine xattr, re-applies a valid local ad-hoc signature
# (codesign -f -s -), and installs the binary.
#
# Env overrides:
#   FAMCLAW_VERSION - release tag to install (default: latest)
#   FAMCLAW_BIN     - destination binary path
#                     (default: ~/.local/bin/famclaw)
#
# This is a helper for developers, not an end-user installer: end users are
# directed to the Raspberry Pi SD images.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
    echo "ERROR: this script installs the macOS build and must run on macOS." >&2
    exit 2
fi

case "$(uname -m)" in
    arm64)          ARCH="arm64" ;;   # Apple Silicon
    x86_64|amd64)   ARCH="amd64" ;;   # Intel Mac
    *) echo "ERROR: unsupported Mac architecture: $(uname -m)" >&2; exit 2 ;;
esac

VERSION="${FAMCLAW_VERSION:-latest}"
DEST="${FAMCLAW_BIN:-$HOME/.local/bin/famclaw}"

if [ "$VERSION" = "latest" ]; then
    BASE="https://github.com/famclaw/famclaw/releases/latest/download"
else
    BASE="https://github.com/famclaw/famclaw/releases/download/${VERSION}"
fi
ARTIFACT="famclaw-darwin-${ARCH}.tar.xz"

if ! command -v shasum >/dev/null 2>&1; then
    echo "ERROR: shasum not found (it ships with macOS)." >&2
    exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "Downloading ${ARTIFACT} (${VERSION})..."
curl -fsSL -o "${WORK}/${ARTIFACT}" "${BASE}/${ARTIFACT}"
curl -fsSL -o "${WORK}/checksums.txt" "${BASE}/checksums.txt"

echo "Verifying checksum..."
grep "  ${ARTIFACT}\$" "${WORK}/checksums.txt" > "${WORK}/one.sum"
( cd "$WORK" && shasum -a 256 -c one.sum )

echo "Extracting..."
tar -xJf "${WORK}/${ARTIFACT}" -C "$WORK"
BIN="${WORK}/famclaw"

# Clear any quarantine attribute a browser might have left on the file, then
# re-apply a valid local ad-hoc signature so the kernel will execute it even
# after the move (an in-place copy would invalidate the shipped signature —
# that is the Killed: 9 / exit 137 case in #214 and #311).
xattr -c "$BIN" 2>/dev/null || true
codesign -f -s - "$BIN"

mkdir -p "$(dirname "$DEST")"
mv "$BIN" "$DEST"
chmod +x "$DEST"

echo "Installed: $("$DEST" --version)"
case ":$PATH:" in
    *":${DEST%/*}:"*) ;;
    *) echo "NOTE: ${DEST%/*} is not on your PATH — use the full path, or add it." ;;
esac
echo
echo "Next: mkdir ~/famclaw && cd ~/famclaw && touch config.yaml"
echo "      ${DEST} --config config.yaml   # then open http://localhost:8080"
echo "macOS builds are developers-only; details in docs/MACOS.md."
