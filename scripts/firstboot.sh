#!/usr/bin/env bash
# FamClaw first boot setup
# Runs once via famclaw-firstboot.service, then disables itself
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

# ── 2. Detect RAM and recommend model ─────────────────────────────────────────
RAM_MB=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
echo "  Detected RAM: ${RAM_MB}MB"

if   [ "$RAM_MB" -ge 7000 ]; then MODEL="llama3.1:8b"
elif [ "$RAM_MB" -ge 3500 ]; then MODEL="llama3.2:3b"
elif [ "$RAM_MB" -ge 1800 ]; then MODEL="phi3:mini"
else                               MODEL="tinyllama"
fi

sed -i "s/WILL_BE_SET_ON_FIRST_BOOT/$MODEL/" "$CONFIG"
echo "✓ Selected model: $MODEL (based on ${RAM_MB}MB RAM)"

# ── 3. Install Ollama ─────────────────────────────────────────────────────────
if ! command -v ollama &>/dev/null; then
  echo "→ Installing Ollama…"
  curl -fsSL https://ollama.com/install.sh | sh
  systemctl enable ollama
  systemctl start ollama
  sleep 5
fi
echo "✓ Ollama ready"

# ── 4. Pull LLM model ─────────────────────────────────────────────────────────
echo "→ Pulling $MODEL (this may take a while on first boot)…"
ollama pull "$MODEL"
echo "✓ Model $MODEL ready"

# ── 5. Set hostname ───────────────────────────────────────────────────────────
HOSTNAME="famclaw"
hostnamectl set-hostname "$HOSTNAME"
sed -i "s/raspberrypi/$HOSTNAME/g" /etc/hosts 2>/dev/null || true
echo "✓ Hostname set to $HOSTNAME"

# ── 6. Enable and start famclaw ───────────────────────────────────────────────
systemctl enable famclaw
systemctl start famclaw
echo "✓ FamClaw service started"

# ── 7. Get network info ───────────────────────────────────────────────────────
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
echo "  Next steps:"
echo "  1. Open the web UI and add your family"
echo "  2. Set up messaging gateways (optional)"
echo "     See: http://famclaw.local:8080/docs"
echo ""
echo "  Logs: journalctl -u famclaw -f"
echo "════════════════════════════════════════════"
