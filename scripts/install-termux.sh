#!/usr/bin/env bash
# FamClaw installer for Android via Termux
# Install Termux from F-Droid: https://f-droid.org/packages/com.termux/
# Then run: curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-termux.sh | bash
#
# FamClaw is a GATEWAY — it does NOT run an LLM locally.
# NEVER install Ollama on Android.
# Configure the LLM endpoint via the web UI after install.
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
pkg install -y curl

# Create directory
mkdir -p "$FAMCLAW_DIR"/{data,skills,policies/family,policies/data}
mkdir -p "$HOME/bin"

# Download binary
RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"
echo "→ Downloading $BINARY…"
curl -fsSL "$RELEASE_BASE/$BINARY" -o "$HOME/bin/famclaw"
chmod +x "$HOME/bin/famclaw"

# Try to install HoneyBadger
HB_INSTALLED=0
HB_URL="https://github.com/famclaw/honeybadger/releases/latest/download/honeybadger-android-arm64"
echo "→ Downloading HoneyBadger…"
if curl -fsSL "$HB_URL" -o "$HOME/bin/honeybadger" 2>/dev/null; then
  chmod +x "$HOME/bin/honeybadger"
  HB_INSTALLED=1
  echo "  HoneyBadger installed ✅"
else
  echo "  HoneyBadger not available — skill scanning disabled"
fi

# Generate minimal config — LLM endpoint left empty for web UI setup
SECRET=$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 24)

cat > "$FAMCLAW_DIR/config.yaml" << YAML
server:
  host: "0.0.0.0"
  port: 8080
  secret: "$SECRET"
  mdns_name: "famclaw"

llm:
  base_url: ""
  model: ""

users:
  - name: "parent"
    display_name: "Parent"
    role: "parent"
    pin: "1234"
    color: "#6366f1"

storage:
  db_path: "$FAMCLAW_DIR/data/famclaw.db"

policies:
  dir: "$FAMCLAW_DIR/policies/family"
  data_dir: "$FAMCLAW_DIR/policies/data"
YAML

# Create start script
cat > "$HOME/bin/famclaw-start" << 'SCRIPT'
#!/usr/bin/env bash
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
echo "  ✅ FamClaw installed!"
echo ""
echo "  Start: famclaw-start"
echo ""
echo "  Then open on any device:"
echo "  http://$IP:8080"
echo ""
echo "  The web UI will guide you through setup:"
echo "  • Configure your LLM endpoint"
echo "  • Add family members"
echo "  • Set up messaging gateways"
echo ""
echo "  Config: ~/famclaw/config.yaml"
echo "════════════════════════════════════════════"
