#!/usr/bin/env bash
# FamClaw installer for Android via Termux
# Install Termux from F-Droid first: https://f-droid.org/packages/com.termux/
# Then run: curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-termux.sh | bash
#
# FamClaw is a GATEWAY — it does NOT run an LLM locally.
# NEVER install Ollama on Android.
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
pkg install -y curl git

# Create directory
mkdir -p "$FAMCLAW_DIR"/{data,skills,policies/family,policies/data}
mkdir -p "$HOME/bin"

# Download binary
RELEASE_BASE="https://github.com/famclaw/famclaw/releases/latest/download"
echo "→ Downloading $BINARY…"
curl -fsSL "$RELEASE_BASE/$BINARY" -o "$HOME/bin/famclaw"
chmod +x "$HOME/bin/famclaw"

# ── Configure LLM endpoint ───────────────────────────────────────────────────
echo ""
echo "FamClaw needs an LLM backend. Where is it running?"
echo ""
echo "  1) Another device on LAN running Ollama"
echo "  2) Cloud API (OpenAI, Anthropic, OpenRouter)"
echo ""
read -rp "Choose (1-2): " LLM_CHOICE

case "$LLM_CHOICE" in
  1)
    read -rp "Ollama URL (e.g. http://192.168.1.10:11434): " LLM_URL
    LLM_URL="${LLM_URL:-http://192.168.1.10:11434}"
    read -rp "Model name (e.g. llama3.2:3b): " LLM_MODEL
    LLM_MODEL="${LLM_MODEL:-llama3.2:3b}"
    API_KEY=""
    ;;
  2)
    read -rp "API base URL (e.g. https://api.openai.com/v1): " LLM_URL
    LLM_URL="${LLM_URL:-https://api.openai.com/v1}"
    read -rp "Model name (e.g. gpt-4o-mini): " LLM_MODEL
    LLM_MODEL="${LLM_MODEL:-gpt-4o-mini}"
    read -rsp "API key: " API_KEY
    echo ""
    ;;
  *)
    LLM_URL="http://192.168.1.10:11434"
    LLM_MODEL="llama3.2:3b"
    API_KEY=""
    echo "Using defaults — edit config later"
    ;;
esac

# Generate config
SECRET=$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 24)

cat > "$FAMCLAW_DIR/config.yaml" << YAML
server:
  host: "0.0.0.0"
  port: 8080
  secret: "$SECRET"
  mdns_name: "famclaw"

llm:
  base_url: "$LLM_URL"
  model: "$LLM_MODEL"
YAML

if [ -n "$API_KEY" ]; then
  echo "  api_key: \"$API_KEY\"" >> "$FAMCLAW_DIR/config.yaml"
fi

cat >> "$FAMCLAW_DIR/config.yaml" << YAML

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
echo "  ⚠  Default parent PIN is 1234"
echo "     Change in ~/famclaw/config.yaml"
echo ""
echo "  LLM: $LLM_MODEL @ $LLM_URL"
echo "════════════════════════════════════════════"
