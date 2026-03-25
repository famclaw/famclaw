# Flashing FamClaw to an SD Card

Get FamClaw running on a Raspberry Pi in under 10 minutes.

## What you need

- Raspberry Pi 3, 4, or 5
- microSD card (16GB minimum, 32GB recommended)
- A computer to flash the SD card
- Network cable or WiFi credentials

---

## Step 1 — Download the image

Go to the [latest release](https://github.com/famclaw/famclaw/releases/latest) and download the image for your Pi:

| Your hardware | Download |
|---|---|
| Raspberry Pi 4 or Pi 5 | `famclaw-rpi4-arm64.img.xz` |
| Raspberry Pi 3, Pi 2, Pi Zero 2W | `famclaw-rpi3-armv7.img.xz` |

Verify the checksum:
```bash
sha256sum -c famclaw-rpi4-arm64.img.xz.sha256
```

---

## Step 2 — Flash the SD card

**Recommended: Raspberry Pi Imager** (free, works on Mac/Windows/Linux)

1. Download [Raspberry Pi Imager](https://www.raspberrypi.com/software/)
2. Click **Choose OS** → **Use custom** → select the `.img.xz` file
3. Click **Choose Storage** → select your SD card
4. Click the **⚙️ gear icon** to set:
   - Hostname: `famclaw`
   - Enable SSH (optional, for advanced users)
   - WiFi credentials (if not using ethernet)
5. Click **Write**

**Alternative: command line**
```bash
xz -d famclaw-rpi4-arm64.img.xz
sudo dd if=famclaw-rpi4-arm64.img of=/dev/sdX bs=4M status=progress
sync
```
Replace `/dev/sdX` with your SD card device (`lsblk` to find it).

---

## Step 3 — First boot

1. Insert the SD card into your Pi
2. Connect ethernet (recommended for first boot)
3. Power on
4. Wait 1–2 minutes — first boot:
   - Generates a secret key
   - Prompts for your LLM endpoint (if terminal attached)
   - Starts FamClaw

You can watch progress via serial console or by SSHing in:
```bash
ssh pi@famclaw.local
sudo journalctl -u famclaw-firstboot -f
```

---

## Step 4 — Open FamClaw

Once the first boot completes, open on **any device on your home network**:

```
http://famclaw.local:8080
```

If `famclaw.local` doesn't work (some Android devices), use the IP address:
```bash
# Find the IP from your router, or from the Pi:
hostname -I
```

---

## Step 5 — Add your family

1. Open the web UI
2. Go to **Settings → Family**
3. Add each family member with their name, age group, and color
4. Link their messaging accounts (Telegram, WhatsApp, Discord) if using gateways

---

## Default credentials

| | Value |
|---|---|
| SSH user | `pi` |
| SSH password | `raspberry` (change this!) |
| Web UI | No login required on LAN |
| Parent dashboard | No PIN yet (coming in v2) |

---

## LLM backend

FamClaw is a **gateway** — it does not run the LLM locally. You need a separate LLM backend:

| Backend | Example URL |
|---------|-------------|
| Ollama on Mac Mini / another Pi | `http://192.168.1.10:11434` |
| OpenAI | `https://api.openai.com/v1` |
| Anthropic | `https://api.anthropic.com/v1` |
| OpenRouter | `https://openrouter.ai/api/v1` |

The first boot script prompts you for the endpoint URL. To change later:
```bash
nano /opt/famclaw/config.yaml   # edit llm.base_url and llm.model
sudo systemctl restart famclaw
```

---

## Troubleshooting

**Can't reach famclaw.local:**
- mDNS can take a minute to propagate
- Try the IP address directly
- On Windows, install [Bonjour](https://support.apple.com/kb/DL999)

**FamClaw not starting:**
- Check logs: `sudo journalctl -u famclaw -f`
- Verify LLM endpoint is reachable from the Pi

**Out of disk space:**
- Use a larger SD card (32GB+)
- Large models (8B+) need significant space
