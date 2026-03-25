#!/usr/bin/env bash
# FamClaw first boot setup
# Runs once via famclaw-firstboot.service, then disables itself
#
# FamClaw is a GATEWAY — it does NOT run an LLM locally.
# The LLM runs on a separate device (another Pi, Mac, or cloud API).
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

# ── 3. Configure LLM endpoint ────────────────────────────────────────────────
# FamClaw is a gateway — the LLM runs elsewhere.
if [ -t 0 ]; then
  echo ""
  echo "FamClaw needs an LLM backend. Where is it running?"
  echo ""
  echo "  1) Another device on LAN running Ollama (e.g. Mac Mini, another Pi)"
  echo "  2) Cloud API (OpenAI, Anthropic, OpenRouter)"
  echo "  3) Skip — I'll configure it later"
  echo ""
  read -rp "Choose (1-3): " LLM_CHOICE

  case "$LLM_CHOICE" in
    1)
      read -rp "Ollama URL (e.g. http://192.168.1.10:11434): " LLM_URL
      LLM_URL="${LLM_URL:-http://192.168.1.10:11434}"
      read -rp "Model name (e.g. llama3.2:3b): " LLM_MODEL
      LLM_MODEL="${LLM_MODEL:-llama3.2:3b}"
      sed -i "s|base_url:.*|base_url: \"$LLM_URL\"|" "$CONFIG"
      sed -i "s|model:.*|model: \"$LLM_MODEL\"|" "$CONFIG"
      echo "✓ LLM: $LLM_MODEL @ $LLM_URL"
      ;;
    2)
      read -rp "API base URL (e.g. https://api.openai.com/v1): " LLM_URL
      LLM_URL="${LLM_URL:-https://api.openai.com/v1}"
      read -rp "Model name (e.g. gpt-4o-mini): " LLM_MODEL
      LLM_MODEL="${LLM_MODEL:-gpt-4o-mini}"
      read -rsp "API key: " API_KEY
      echo ""
      sed -i "s|base_url:.*|base_url: \"$LLM_URL\"|" "$CONFIG"
      sed -i "s|model:.*|model: \"$LLM_MODEL\"|" "$CONFIG"
      # Append api_key to llm section
      sed -i "/^  model:/a\\  api_key: \"$API_KEY\"" "$CONFIG"
      echo "✓ LLM: $LLM_MODEL @ $LLM_URL (cloud)"
      ;;
    *)
      echo "⚠ Skipped LLM setup — edit $CONFIG later"
      ;;
  esac
else
  echo "⚠ No interactive terminal — using default LLM config"
  echo "  Edit $CONFIG to set your LLM endpoint"
fi

# ── 4. Enable and start famclaw ──────────────────────────────────────────────
systemctl enable famclaw
systemctl start famclaw
echo "✓ FamClaw service started"

# ── 5. Get network info ──────────────────────────────────────────────────────
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
echo "  Config: $CONFIG"
echo "  Logs:   journalctl -u famclaw -f"
echo "════════════════════════════════════════════"
