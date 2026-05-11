#!/usr/bin/env bash
# FamClaw updater — cross-platform (Linux systemd + macOS launchd).
#
# Downloads the latest release for the current OS/arch, verifies the goreleaser
# checksums.txt entry, extracts the tar.xz, swaps the binary atomically, and
# restarts the service via the platform's service manager. On startup failure
# within $TIMEOUT seconds, rolls back to the previous binary.
#
# Env overrides:
#   FAMCLAW_BIN     - target binary path (default: /usr/local/bin/famclaw)
#   FAMCLAW_VERSION - specific tag to install (default: latest)
#   TIMEOUT         - startup wait in seconds (default: 30)

set -euo pipefail

# ── Platform detection ───────────────────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
RAW_ARCH=$(uname -m)
case "$RAW_ARCH" in
    aarch64|arm64) ARCH="arm64" ;;
    armv7l)        ARCH="armv7" ;;
    x86_64|amd64)  ARCH="amd64" ;;
    *) echo "ERROR: unsupported architecture: $RAW_ARCH" >&2; exit 2 ;;
esac
case "$OS" in
    linux|darwin) ;;
    *) echo "ERROR: unsupported OS: $OS (this updater supports linux and darwin)" >&2; exit 2 ;;
esac

BINARY="${FAMCLAW_BIN:-/usr/local/bin/famclaw}"
TIMEOUT="${TIMEOUT:-30}"
VERSION="${FAMCLAW_VERSION:-latest}"

if [ "$VERSION" = "latest" ]; then
    RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"
else
    RELEASE_BASE="https://github.com/famclaw/famclaw/releases/download/${VERSION}"
fi

ARTIFACT_BASE="famclaw-${OS}-${ARCH}"
ARTIFACT_ARCHIVE="${ARTIFACT_BASE}.tar.xz"

# ── Platform-specific service control ────────────────────────────────────────
if [ "$OS" = "darwin" ]; then
    PLIST="${HOME}/Library/LaunchAgents/com.famclaw.famclaw.plist"
    SERVICE_LABEL="com.famclaw.famclaw"
    DOMAIN="gui/$(id -u)"

    if [ ! -f "$PLIST" ]; then
        echo "ERROR: launchd plist not found at $PLIST" >&2
        echo "       Install com.famclaw.plist (substituting FAMCLAW_DIR + CURRENT_USER) before running this updater." >&2
        exit 3
    fi

    service_stop()   { launchctl bootout "${DOMAIN}/${SERVICE_LABEL}" 2>/dev/null || true; }
    service_start()  { launchctl bootstrap "$DOMAIN" "$PLIST"; }
    service_active() { launchctl print "${DOMAIN}/${SERVICE_LABEL}" >/dev/null 2>&1; }
else
    SERVICE="${FAMCLAW_SERVICE:-famclaw}"
    SERVICE_LABEL="$SERVICE"

    service_stop()   { sudo systemctl stop "$SERVICE" 2>/dev/null || true; }
    service_start()  { sudo systemctl start "$SERVICE"; }
    service_active() { systemctl is-active --quiet "$SERVICE"; }
fi

# Need sudo to write the binary if the target dir isn't user-writable
if [ -w "$(dirname "$BINARY")" ]; then SUDO=""; else SUDO="sudo"; fi

echo "FamClaw updater"
echo "  Platform: ${OS}/${ARCH}"
echo "  Version:  ${VERSION}"
echo "  Binary:   ${BINARY}"
echo "  Service:  ${SERVICE_LABEL}"
echo

# ── Backup current binary ────────────────────────────────────────────────────
BACKUP="${TMPDIR:-/tmp}/famclaw-previous-$$"
if [ -f "$BINARY" ]; then
    cp "$BINARY" "$BACKUP"
    echo "  Backed up current binary to $BACKUP"
fi

# ── Download artifact + checksums ────────────────────────────────────────────
TMP=$(mktemp -d)
trap "rm -rf '$TMP'" EXIT

echo "  Downloading ${ARTIFACT_ARCHIVE}..."
curl -fsSL "${RELEASE_BASE}/${ARTIFACT_ARCHIVE}" -o "${TMP}/${ARTIFACT_ARCHIVE}"

echo "  Downloading checksums.txt..."
curl -fsSL "${RELEASE_BASE}/checksums.txt" -o "${TMP}/checksums.txt"

# ── Verify checksum (goreleaser convention: single checksums.txt with all hashes) ──
echo "  Verifying checksum..."
(
    cd "$TMP"
    # Extract just the line for our artifact and pipe it to sha256sum -c
    if awk -v f="$ARTIFACT_ARCHIVE" '$NF == f' checksums.txt | sha256sum -c --status; then
        echo "  ✓ Checksum verified"
    else
        echo "  ✗ Checksum mismatch — aborting" >&2
        exit 1
    fi
)

# ── Extract (tar.xz contains the famclaw binary + maybe LICENSE/README) ──────
echo "  Extracting archive..."
mkdir -p "${TMP}/extract"
tar -xJf "${TMP}/${ARTIFACT_ARCHIVE}" -C "${TMP}/extract"

NEW_BIN=$(find "${TMP}/extract" -name "famclaw" -type f -perm -u+x | head -1)
if [ -z "$NEW_BIN" ]; then
    # Fallback: file might not have +x on tar extract; pick the binary by name regardless
    NEW_BIN=$(find "${TMP}/extract" -name "famclaw" -type f | head -1)
fi
if [ -z "$NEW_BIN" ] || [ ! -f "$NEW_BIN" ]; then
    echo "ERROR: famclaw binary not found inside archive — extracted contents:" >&2
    find "${TMP}/extract" -maxdepth 3 >&2
    exit 1
fi
chmod +x "$NEW_BIN"

# ── Stop, install, start ─────────────────────────────────────────────────────
echo "  Stopping service..."
service_stop

echo "  Installing new binary to ${BINARY}..."
$SUDO mkdir -p "$(dirname "$BINARY")"
$SUDO cp "$NEW_BIN" "$BINARY"
$SUDO chmod +x "$BINARY"

echo "  Starting service..."
service_start

# ── Verify startup ───────────────────────────────────────────────────────────
echo "  Waiting up to ${TIMEOUT}s for service to become active..."
STARTED=false
for _ in $(seq 1 "$TIMEOUT"); do
    if service_active; then STARTED=true; break; fi
    sleep 1
done

if [ "$STARTED" = true ]; then
    VERSION_OUT=$("$BINARY" --version 2>&1 | head -1 || echo "unknown")
    echo
    echo "  ✓ Updated successfully — running version: $VERSION_OUT"
    exit 0
fi

# ── Rollback on failure ──────────────────────────────────────────────────────
echo
echo "  ✗ New binary failed to become active within ${TIMEOUT}s — rolling back..." >&2
if [ -f "$BACKUP" ]; then
    $SUDO cp "$BACKUP" "$BINARY"
    $SUDO chmod +x "$BINARY"
    service_start
    if service_active; then
        echo "  ↻ Rolled back to previous binary." >&2
    else
        echo "  ⚠ Rollback also failed — manual intervention needed." >&2
    fi
else
    echo "  ⚠ No backup available — manual intervention needed." >&2
fi
exit 1
