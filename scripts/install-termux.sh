#!/usr/bin/env bash
# FamClaw installer for Android via Termux
# Install Termux from F-Droid first: https://f-droid.org/packages/com.termux/
# Then run: curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-termux.sh | bash
set -euo pipefail

ARCH=$(uname -m)
FAMCLAW_DIR="$HOME/famclaw"

case "$ARCH" in
  aarch64) BINARY="famclaw-android-arm64" ;;
  armv7l)  BINARY="famclaw-android-armv7"  ;;
  *)       echo "Unsupported: $ARCH"; exit 1 ;;
esac

echo "════════════════════════════════════════════"
echo "  FamClaw for Android (Termux)"
echo "  Arch: $ARCH"
echo "════════════════════════════════════════════"

# Termux packages
pkg update -y
pkg install -y curl git termux-api

# Check available RAM
RAM_MB=$(free -m | awk '/Mem:/ {print $2}')
echo "→ Available RAM: ${RAM_MB}MB"

# Install Ollama (Termux version)
if ! command -v ollama &>/dev/null; then
  echo "→ Installing Ollama…"
  curl -fsSL https://ollama.com/install.sh | sh
fi

# Choose model
if   [ "$RAM_MB" -ge 6000 ]; then MODEL="phi3:mini"
elif [ "$RAM_MB" -ge 3000 ]; then MODEL="tinyllama"
else
  echo "⚠️  Low RAM detected. Using tinyllama (smallest available model)."
  MODEL="tinyllama"
fi

echo "→ Pulling model: $MODEL (this may take a while on mobile…)"
ollama pull "$MODEL"

# Create directory
mkdir -p "$FAMCLAW_DIR"/{data,skills,policies/family,policies/data}

# Download binary
RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"
echo "→ Downloading $BINARY…"
curl -fsSL "$RELEASE_BASE/$BINARY" -o "$HOME/bin/famclaw"
chmod +x "$HOME/bin/famclaw"

# Default config
SECRET=$(head -c 24 /dev/random | base64 | tr -d '/+=' | head -c 24)
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
  - name: "child"
    display_name: "Child"
    role: "child"
    age_group: "age_8_12"
    color: "#f59e0b"

storage:
  db_path: "$FAMCLAW_DIR/data/famclaw.db"

policies:
  dir: "$FAMCLAW_DIR/policies/family"
  data_dir: "$FAMCLAW_DIR/policies/data"
YAML

# Create start script
cat > "$HOME/bin/famclaw-start" << 'SCRIPT'
#!/usr/bin/env bash
# Start Ollama then FamClaw
pkill ollama 2>/dev/null; sleep 1
ollama serve &
sleep 3
cd ~/famclaw && famclaw --config config.yaml
SCRIPT
chmod +x "$HOME/bin/famclaw-start"

# Termux boot (if termux-boot is installed)
mkdir -p "$HOME/.termux/boot"
cat > "$HOME/.termux/boot/famclaw.sh" << 'BOOT'
#!/data/data/com.termux/files/usr/bin/bash
~/bin/famclaw-start
BOOT
chmod +x "$HOME/.termux/boot/famclaw.sh"

IP=$(ip route get 1.1.1.1 2>/dev/null | grep -oP 'src \K[^ ]+' || echo "your-phone-ip")

echo ""
echo "════════════════════════════════════════════"
echo "  ✅ FamClaw for Android ready!"
echo ""
echo "  Start: famclaw-start"
echo ""
echo "  Then open on any device:"
echo "  http://$IP:8080"
echo ""
echo "  ⚠️  Default parent PIN is 1234"
echo "      Change in ~/famclaw/config.yaml"
echo "════════════════════════════════════════════"
