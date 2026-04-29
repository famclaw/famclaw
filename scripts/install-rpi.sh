#!/usr/bin/env bash
# FamClaw Raspberry Pi installer
# Usage: curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-rpi.sh | bash
set -euo pipefail

ARCH=$(uname -m)
FAMCLAW_DIR="/opt/famclaw"
FAMCLAW_USER="famclaw"
RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"

# Detect binary name
case "$ARCH" in
  aarch64|arm64) BINARY="famclaw-linux-arm64" ;;
  armv7l)        BINARY="famclaw-linux-armv7"  ;;
  x86_64)        BINARY="famclaw-linux-amd64"  ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

echo "════════════════════════════════════════════"
echo "  FamClaw Installer"
echo "  Architecture: $ARCH → $BINARY"
echo "════════════════════════════════════════════"

# Install Ollama for local LLM inference
if ! command -v ollama &>/dev/null; then
  echo "→ Installing Ollama…"
  curl -fsSL https://ollama.com/install.sh | sh
  systemctl enable ollama
  systemctl start ollama
fi

# Pull recommended model based on RAM
RAM_MB=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
echo "→ Detected ${RAM_MB}MB RAM"
if   [ "$RAM_MB" -ge 8192 ]; then MODEL="llama3.1:8b"
elif [ "$RAM_MB" -ge 4096 ]; then MODEL="llama3.2:3b"
elif [ "$RAM_MB" -ge 2048 ]; then MODEL="phi3:mini"
else                               MODEL="tinyllama"
fi
echo "→ Recommended model: $MODEL"
ollama pull "$MODEL"

# Create user and directories
echo "→ Creating famclaw user and directories…"
id -u "$FAMCLAW_USER" &>/dev/null || useradd -r -s /bin/false "$FAMCLAW_USER"
mkdir -p "$FAMCLAW_DIR"/{data,skills}
chown -R "$FAMCLAW_USER:$FAMCLAW_USER" "$FAMCLAW_DIR"

# Download binary
echo "→ Downloading famclaw ($BINARY)…"
curl -fsSL "$RELEASE_BASE/$BINARY" -o /usr/local/bin/famclaw
chmod +x /usr/local/bin/famclaw

# Download HoneyBadger for security scanning
HB_INSTALLED=0
HB_RELEASE_BASE="https://github.com/famclaw/honeybadger/releases/latest/download"
case "$ARCH" in
  aarch64|arm64) HB_BINARY="honeybadger-linux-arm64" ;;
  armv7l|armhf)  HB_BINARY="honeybadger-linux-armv7" ;;
  x86_64|amd64)  HB_BINARY="honeybadger-linux-amd64" ;;
  *)             HB_BINARY="" ;;
esac

if [ -n "$HB_BINARY" ]; then
  echo "→ Downloading HoneyBadger ($HB_BINARY)…"
  if curl -fsSL "$HB_RELEASE_BASE/$HB_BINARY" -o /usr/local/bin/honeybadger 2>/dev/null; then
    chmod +x /usr/local/bin/honeybadger
    HB_INSTALLED=1
    echo "  HoneyBadger installed ✅"
  else
    echo ""
    echo "================================================================"
    echo "  WARNING: Could not download HoneyBadger."
    echo "  Skill security scanning will be disabled."
    echo "  Install manually:"
    echo "    go install github.com/famclaw/honeybadger/cmd/honeybadger@latest"
    echo "  Then set seccheck.auto_seccheck: true in config.yaml"
    echo "================================================================"
    echo ""
  fi
fi

# Generate default config if not present
if [ ! -f "$FAMCLAW_DIR/config.yaml" ]; then
  echo "→ Writing default config…"
  SECRET=$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 32)
  cat > "$FAMCLAW_DIR/config.yaml" << YAML
server:
  host: "0.0.0.0"
  port: 8080
  secret: "$SECRET"
  mdns_name: "famclaw"

llm:
  provider: "ollama"
  base_url: "http://localhost:11434"
  model: "$MODEL"

users:
  - name: "parent"
    display_name: "Parent"
    role: "parent"
    pin: "1234"
    color: "#6366f1"

  - name: "child1"
    display_name: "Child"
    role: "child"
    age_group: "age_8_12"
    color: "#f59e0b"

storage:
  db_path: "./data/famclaw.db"

seccheck:
  enabled: $([ "$HB_INSTALLED" = "1" ] && echo "true" || echo "false")
  auto_seccheck: $([ "$HB_INSTALLED" = "1" ] && echo "true" || echo "false")  # Auto-disabled: honeybadger not available during setup
  block_on_fail: true
  paranoia: "family"
  runtime_scan: $([ "$HB_INSTALLED" = "1" ] && echo "true" || echo "false")
  rescan_interval: "168h"
  quarantine_on_fail: true
  notify_on_quarantine: true
YAML
  chown "$FAMCLAW_USER:$FAMCLAW_USER" "$FAMCLAW_DIR/config.yaml"
  chmod 640 "$FAMCLAW_DIR/config.yaml"
  echo ""
  echo "⚠️  Default parent PIN is 1234 — change it in $FAMCLAW_DIR/config.yaml"
  echo ""
fi

# Install systemd service
echo "→ Installing systemd service…"
cat > /etc/systemd/system/famclaw.service << SERVICE
[Unit]
Description=FamClaw Family AI Assistant
After=network-online.target ollama.service
Wants=network-online.target

[Service]
Type=simple
User=$FAMCLAW_USER
WorkingDirectory=$FAMCLAW_DIR
ExecStart=/usr/local/bin/famclaw --config $FAMCLAW_DIR/config.yaml
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SERVICE

systemctl daemon-reload
systemctl enable famclaw
systemctl start famclaw

# Get local IP
IP=$(hostname -I | awk '{print $1}')

echo ""
echo "════════════════════════════════════════════"
echo "  ✅ FamClaw installed and running!"
echo ""
echo "  Open on any device on your network:"
echo "  http://famclaw.local:8080"
echo "  http://$IP:8080"
echo ""
echo "  Logs:   journalctl -u famclaw -f"
echo "  Config: $FAMCLAW_DIR/config.yaml"
echo "════════════════════════════════════════════"
