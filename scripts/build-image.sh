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
  arm64) BASE_IMG_SHA256="6ac3a10a1f144c7e9d1f8e568d75ca809288280a593eb6ca053e49b539f465a4"; BASE_IMG_URL="${BASE_URL}_arm64/images/raspios_lite_arm64-2024-11-19/2024-11-19-raspios-bookworm-arm64-lite.img.xz" ;;
  armhf) BASE_IMG_SHA256="1dacc70cc09162c995020ac1739ee60e6843cf2bfb393654e694e3f017d781bb"; BASE_IMG_URL="${BASE_URL}_armhf/images/raspios_lite_armhf-2024-11-19/2024-11-19-raspios-bookworm-armhf-lite.img.xz" ;;
  *) echo "Unknown arch: $ARCH"; exit 1 ;;
esac

WORK_DIR=$(mktemp -d)
LOOP=""
ROOT_MOUNTED=0
BOOT_MOUNTED=0
cleanup() {
  local status=$?
  trap - EXIT
  set +e
  local cleanup_ok=1
  if (( BOOT_MOUNTED )); then umount "$MOUNT_BOOT" || cleanup_ok=0; fi
  if (( ROOT_MOUNTED )); then umount "$MOUNT_ROOT" || cleanup_ok=0; fi
  if [[ -n "$LOOP" ]]; then losetup -d "$LOOP" || cleanup_ok=0; fi
  if (( cleanup_ok )); then
    rm -rf -- "$WORK_DIR" || cleanup_ok=0
  fi
  # Preserve an existing error; cleanup failure must also block publication.
  if (( status == 0 && ! cleanup_ok )); then status=1; fi
  exit "$status"
}
trap cleanup EXIT

echo "════════════════════════════════════════════"
echo "  Building FamClaw $VERSION SD image"
echo "  Arch: $ARCH → $OUTPUT"
echo "════════════════════════════════════════════"

# Download base image
echo "→ Downloading Raspberry Pi OS Lite ($ARCH)…"
BASE_IMG="$WORK_DIR/base.img.xz"
curl -fsSL "$BASE_IMG_URL" -o "$BASE_IMG"
printf '%s  %s\n' "$BASE_IMG_SHA256" "$BASE_IMG" | sha256sum --check --status
xz -d "$BASE_IMG"
BASE_IMG="${BASE_IMG%.xz}"

# Resize image (+512MB for famclaw + models space hint)
echo "→ Extending image…"
dd if=/dev/zero bs=1M count=512 >> "$BASE_IMG"
cp "$BASE_IMG" "$OUTPUT"

# Attach and expand the root partition to use the space appended above.
echo "→ Expanding root partition…"
LOOP=$(losetup -f --show -P "$OUTPUT")
partprobe "$LOOP"
BOOT_PART="${LOOP}p1"
ROOT_PART="${LOOP}p2"
for attempt in {1..10}; do
  if blockdev --getsize64 "$ROOT_PART" >/dev/null 2>&1; then break; fi
  if (( attempt == 10 )); then echo "Root partition did not appear" >&2; exit 1; fi
  sleep 1
done
old_partition_bytes=$(blockdev --getsize64 "$ROOT_PART")
parted -s "$LOOP" resizepart 2 100%
partprobe "$LOOP"
for attempt in {1..10}; do
  new_partition_bytes=$(blockdev --getsize64 "$ROOT_PART")
  if (( new_partition_bytes > old_partition_bytes )); then break; fi
  if (( attempt == 10 )); then echo "Root partition did not grow" >&2; exit 1; fi
  sleep 1
done

# e2fsck permits 0 (clean) or 1 (errors corrected); all other statuses fail.
if e2fsck -f -p "$ROOT_PART"; then
  :
else
  status=$?
  if (( status != 1 )); then echo "Root filesystem check failed: $status" >&2; exit "$status"; fi
fi
old_blocks=$(dumpe2fs -h "$ROOT_PART" 2>/dev/null | awk '/^Block count:/ {print $3}')
[[ "$old_blocks" =~ ^[0-9]+$ ]]
resize2fs "$ROOT_PART"
new_blocks=$(dumpe2fs -h "$ROOT_PART" 2>/dev/null | awk '/^Block count:/ {print $3}')
[[ "$new_blocks" =~ ^[0-9]+$ ]]
(( new_blocks > old_blocks ))

MOUNT_ROOT="$WORK_DIR/root"
MOUNT_BOOT="$WORK_DIR/boot"
mkdir -p "$MOUNT_ROOT" "$MOUNT_BOOT"
mount "$ROOT_PART" "$MOUNT_ROOT"
ROOT_MOUNTED=1
mount "$BOOT_PART" "$MOUNT_BOOT"
BOOT_MOUNTED=1

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
