#!/usr/bin/env bash
# FamClaw updater — downloads latest, verifies, installs, rolls back on failure.
set -euo pipefail

BINARY="/usr/local/bin/famclaw"
BACKUP="/tmp/famclaw-previous"
SERVICE="famclaw"
RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"
TIMEOUT=30

# Detect platform
ARCH=$(uname -m)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$ARCH" in
    aarch64|arm64) ARCH="arm64" ;;
    armv7l)        ARCH="armv7" ;;
    x86_64)        ARCH="amd64" ;;
esac
ARTIFACT="famclaw-${OS}-${ARCH}"

echo "Updating FamClaw..."
echo "  Platform: ${OS}/${ARCH}"
echo "  Binary:   ${BINARY}"

# ── Backup current ────────────────────────────────────────────────────────────
if [ -f "$BINARY" ]; then
    cp "$BINARY" "$BACKUP"
    echo "  Backed up current binary to $BACKUP"
fi

# ── Download latest ───────────────────────────────────────────────────────────
echo "  Downloading ${ARTIFACT}..."
TMP=$(mktemp -d)
trap "rm -rf $TMP" EXIT

curl -fsSL "${RELEASE_BASE}/${ARTIFACT}.xz" -o "${TMP}/${ARTIFACT}.xz"
curl -fsSL "${RELEASE_BASE}/${ARTIFACT}.xz.sha256" -o "${TMP}/${ARTIFACT}.xz.sha256" 2>/dev/null || true

# ── Verify checksum ───────────────────────────────────────────────────────────
if [ -f "${TMP}/${ARTIFACT}.xz.sha256" ]; then
    cd "$TMP"
    if sha256sum -c "${ARTIFACT}.xz.sha256" >/dev/null 2>&1; then
        echo "  Checksum verified"
    else
        echo "  ERROR: Checksum mismatch — aborting"
        exit 1
    fi
    cd - >/dev/null
fi

# ── Decompress ────────────────────────────────────────────────────────────────
xz -d "${TMP}/${ARTIFACT}.xz"
chmod +x "${TMP}/${ARTIFACT}"

# ── Stop, install, start ─────────────────────────────────────────────────────
echo "  Stopping service..."
sudo systemctl stop "$SERVICE" 2>/dev/null || true

echo "  Installing new binary..."
sudo cp "${TMP}/${ARTIFACT}" "$BINARY"

echo "  Starting service..."
sudo systemctl start "$SERVICE"

# ── Verify startup ────────────────────────────────────────────────────────────
echo "  Waiting ${TIMEOUT}s for startup..."
STARTED=false
for i in $(seq 1 $TIMEOUT); do
    if systemctl is-active --quiet "$SERVICE"; then
        STARTED=true
        break
    fi
    sleep 1
done

if [ "$STARTED" = true ]; then
    VERSION=$(${BINARY} --version 2>&1 | head -1 || echo "unknown")
    echo ""
    echo "  Updated successfully!"
    echo "  Version: $VERSION"
else
    echo ""
    echo "  ERROR: New binary failed to start within ${TIMEOUT}s"
    echo "  Rolling back to previous version..."
    if [ -f "$BACKUP" ]; then
        sudo cp "$BACKUP" "$BINARY"
        sudo systemctl start "$SERVICE"
        echo "  Rolled back. Previous version restored."
    else
        echo "  No backup available — manual intervention needed."
    fi
    exit 1
fi
