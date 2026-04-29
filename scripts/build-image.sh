#!/usr/bin/env bash
# Build a flashable Raspberry Pi SD card image containing FamClaw
# Usage: sudo ./build-image.sh <binary> <arch: arm64|armhf> <output.img> <version>
set -euo pipefail

BINARY="${1:?Usage: $0 <binary> <arch> <output.img> <version>}"
ARCH="${2:?}"
OUTPUT="${3:?}"
VERSION="${4:-dev}"

FAMCLAW_DIR="/opt/famclaw"
BASE_URL="https://downloads.raspberrypi.com/raspios_lite"

# Choose correct base image
case "$ARCH" in
  arm64) BASE_IMG_URL="${BASE_URL}_arm64/images/raspios_lite_arm64-2024-11-19/2024-11-19-raspios-bookworm-arm64-lite.img.xz" ;;
  armhf) BASE_IMG_URL="${BASE_URL}_armhf/images/raspios_lite_armhf-2024-11-19/2024-11-19-raspios-bookworm-armhf-lite.img.xz" ;;
  *) echo "Unknown arch: $ARCH"; exit 1 ;;
esac

WORK_DIR=$(mktemp -d)
trap "rm -rf $WORK_DIR" EXIT

echo "════════════════════════════════════════════"
echo "  Building FamClaw $VERSION SD image"
echo "  Arch: $ARCH → $OUTPUT"
echo "════════════════════════════════════════════"

# Download base image
echo "→ Downloading Raspberry Pi OS Lite ($ARCH)…"
BASE_IMG="$WORK_DIR/base.img.xz"
curl -fsSL "$BASE_IMG_URL" -o "$BASE_IMG"
xz -d "$BASE_IMG"
BASE_IMG="${BASE_IMG%.xz}"

# Resize image (+512MB for famclaw + models space hint)
echo "→ Extending image…"
dd if=/dev/zero bs=1M count=512 >> "$BASE_IMG"
cp "$BASE_IMG" "$OUTPUT"

# Mount partitions
echo "→ Mounting partitions…"
LOOP=$(losetup -f --show -P "$OUTPUT")
partprobe "$LOOP"
sleep 1

BOOT_PART="${LOOP}p1"
ROOT_PART="${LOOP}p2"

# Resize root filesystem
e2fsck -f "$ROOT_PART" || true
resize2fs "$ROOT_PART"

MOUNT_ROOT="$WORK_DIR/root"
MOUNT_BOOT="$WORK_DIR/boot"
mkdir -p "$MOUNT_ROOT" "$MOUNT_BOOT"
mount "$ROOT_PART" "$MOUNT_ROOT"
mount "$BOOT_PART" "$MOUNT_BOOT"

cleanup() {
  umount "$MOUNT_BOOT" 2>/dev/null || true
  umount "$MOUNT_ROOT" 2>/dev/null || true
  losetup -d "$LOOP" 2>/dev/null || true
}
trap cleanup EXIT

# Enable SSH by default
touch "$MOUNT_BOOT/ssh"

# Install famclaw binary
echo "→ Installing famclaw binary…"
install -m 755 "$BINARY" "$MOUNT_ROOT/usr/local/bin/famclaw"

# Create directory structure
mkdir -p "$MOUNT_ROOT$FAMCLAW_DIR"/{data,skills}

# Install default config
cat > "$MOUNT_ROOT$FAMCLAW_DIR/config.yaml" << 'YAML'
# FamClaw configuration
# Edit this file to add your family members and gateway tokens
# Full documentation: https://github.com/famclaw/famclaw/docs/

server:
  host: "0.0.0.0"
  port: 8080
  secret: "WILL_BE_GENERATED_ON_FIRST_BOOT"
  mdns_name: "famclaw"

llm:
  provider: "ollama"
  base_url: "http://localhost:11434"
  model: "WILL_BE_SET_ON_FIRST_BOOT"

users:
  - name: "parent"
    display_name: "Parent"
    role: "parent"
    color: "#6366f1"

storage:
  db_path: "/opt/famclaw/data/famclaw.db"

seccheck:
  sandbox: "auto"
YAML

# Install systemd service
cat > "$MOUNT_ROOT/etc/systemd/system/famclaw.service" << 'SERVICE'
[Unit]
Description=FamClaw Family AI Assistant
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=famclaw
Group=famclaw
WorkingDirectory=/opt/famclaw
EnvironmentFile=-/opt/famclaw/.env
ExecStart=/usr/local/bin/famclaw --config /opt/famclaw/config.yaml
Restart=on-failure
RestartSec=15
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SERVICE

# Install firstboot service
install -m 755 "$(dirname "$0")/firstboot.sh" "$MOUNT_ROOT/usr/local/bin/famclaw-firstboot"
cat > "$MOUNT_ROOT/etc/systemd/system/famclaw-firstboot.service" << 'SERVICE'
[Unit]
Description=FamClaw First Boot Setup
After=network-online.target
Wants=network-online.target
ConditionPathExists=!/opt/famclaw/.firstboot-done

[Service]
Type=oneshot
ExecStart=/usr/local/bin/famclaw-firstboot
ExecStartPost=/bin/touch /opt/famclaw/.firstboot-done
RemainAfterExit=yes
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target
SERVICE

# Enable services via symlink (same as systemctl enable)
WANTS="$MOUNT_ROOT/etc/systemd/system/multi-user.target.wants"
mkdir -p "$WANTS"
ln -sf /etc/systemd/system/famclaw-firstboot.service "$WANTS/famclaw-firstboot.service"
# famclaw.service started by firstboot after setup

# Create famclaw user
echo "famclaw:x:1001:1001:FamClaw,,,:/opt/famclaw:/bin/false" >> "$MOUNT_ROOT/etc/passwd"
echo "famclaw:x:1001:" >> "$MOUNT_ROOT/etc/group"
chown -R 1001:1001 "$MOUNT_ROOT$FAMCLAW_DIR"

# Write version file
echo "$VERSION" > "$MOUNT_ROOT$FAMCLAW_DIR/VERSION"

echo "→ Image built: $OUTPUT"
echo "   Size: $(du -sh "$OUTPUT" | cut -f1)"
echo "   Flash with: Raspberry Pi Imager → Use custom image"
