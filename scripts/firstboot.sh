#!/usr/bin/env bash
# FamClaw first boot setup
# Runs once via famclaw-firstboot.service, then disables itself.
#
# FamClaw is a GATEWAY — it does NOT run an LLM locally.
# The user configures the LLM endpoint via the web UI after first boot.
set -euo pipefail

FAMCLAW_DIR="/opt/famclaw"
CONFIG="$FAMCLAW_DIR/config.yaml"
LOG="/var/log/famclaw-firstboot.log"

exec > >(tee -a "$LOG") 2>&1

echo "════════════════════════════════════════════"
echo "  FamClaw First Boot Setup"
echo "  $(date)"
echo "════════════════════════════════════════════"

# ── 1. Generate secret key ────────────────────────────────────────────────────
SECRET=$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 32)
sed -i "s/WILL_BE_GENERATED_ON_FIRST_BOOT/$SECRET/" "$CONFIG"
echo "✓ Generated secret key"

# ── 2. Set hostname ──────────────────────────────────────────────────────────
HOSTNAME="famclaw"
hostnamectl set-hostname "$HOSTNAME"
sed -i "s/raspberrypi/$HOSTNAME/g" /etc/hosts 2>/dev/null || true
echo "✓ Hostname set to $HOSTNAME"

# ── 3. Enable and start famclaw ──────────────────────────────────────────────
systemctl enable famclaw
systemctl start famclaw
echo "✓ FamClaw service started"

# ── 4. Get network info ──────────────────────────────────────────────────────
sleep 3
IP=$(hostname -I | awk '{print $1}')

echo ""
echo "════════════════════════════════════════════"
echo "  ✅ FamClaw is ready!"
echo ""
echo "  Open on any device on your network:"
echo "  → http://famclaw.local:8080"
echo "  → http://$IP:8080"
echo ""
echo "  The web UI will guide you through setup:"
echo "  • Configure your LLM endpoint"
echo "  • Add family members"
echo "  • Set up messaging gateways"
echo ""
echo "  Config: $CONFIG"
echo "  Logs:   journalctl -u famclaw -f"
echo "════════════════════════════════════════════"
