# Android Setup (Termux)

Run FamClaw on an old Android phone using Termux. The phone acts as the gateway and policy engine — the LLM runs on a separate device.

## Requirements

- Android phone (ARM64 or ARMv7)
- [Termux](https://f-droid.org/packages/com.termux/) — install from F-Droid, **not** Google Play
- A separate LLM backend (RPi with Ollama, Mac Mini, or cloud API)

**Do NOT install Ollama on Android.** It's not supported and will not work.

---

## Installation

```bash
# Open Termux and run:
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-termux.sh | bash
```

Or manually:

```bash
pkg update && pkg upgrade -y
pkg install -y wget

# Download the binary
ARCH=$(uname -m)
case "$ARCH" in
    aarch64) BINARY="famclaw-android-arm64" ;;
    armv7l)  BINARY="famclaw-android-armv7" ;;
    *)       echo "Unsupported arch: $ARCH"; exit 1 ;;
esac

wget "https://github.com/famclaw/famclaw/releases/latest/download/${BINARY}"
chmod +x "$BINARY"
mv "$BINARY" "$PREFIX/bin/famclaw"
```

---

## Configuration

FamClaw on Android needs an external LLM backend. The install script will ask you to choose:

### Option 1: Another device's Ollama on LAN

If you have a Pi or Mac running Ollama:

```yaml
llm:
  base_url: "http://192.168.1.50:11434"   # your Pi/Mac's IP
  model: "llama3.2:3b"
  # no api_key needed for Ollama
```

### Option 2: Cloud provider

```yaml
llm:
  base_url: "https://api.openai.com/v1"
  model: "gpt-4o-mini"
  api_key: "sk-..."
```

Or use Anthropic, OpenRouter, or any OpenAI-compatible API.

---

## Running

```bash
famclaw --config ~/famclaw/config.yaml
```

To run in the background:

```bash
nohup famclaw --config ~/famclaw/config.yaml > ~/famclaw/famclaw.log 2>&1 &
```

To auto-start on device boot, install [Termux:Boot](https://f-droid.org/packages/com.termux.boot/) from F-Droid. The install script creates `~/.termux/boot/famclaw.sh` automatically.

If Termux:Boot is not available, you can start manually each time you open Termux:
```bash
famclaw-start
```

---

## Accessing the web UI

From any device on the same network:

```text
http://<phone-ip>:8080
```

Find the phone's IP:
```bash
ifconfig wlan0 | grep inet
```

---

## Limitations on Android

| Feature | Status |
|---------|--------|
| Web UI | Works |
| Telegram bot | Works |
| Discord bot | Works |
| WhatsApp bot | Not supported (whatsmeow needs persistent storage) |
| mDNS (famclaw.local) | Not reliable on Android |
| Ollama (local LLM) | **Not supported** — use external backend |
| systemd service | Not available — use Termux:Boot or nohup |

---

## Updating

```bash
# Detect architecture and download matching binary
ARCH=$(uname -m)
case "$ARCH" in
    aarch64) BIN="famclaw-android-arm64" ;;
    armv7l)  BIN="famclaw-android-armv7" ;;
esac

curl -fsSL "https://github.com/famclaw/famclaw/releases/latest/download/$BIN" -o "$PREFIX/bin/famclaw"
chmod +x "$PREFIX/bin/famclaw"

# Restart
pkill famclaw
famclaw-start &
```
