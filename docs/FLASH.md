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
4. Wait 2–5 minutes — first boot:
   - Installs Ollama
   - Downloads the AI model (this takes a while depending on your internet speed)
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

## Model recommendations by Pi model

| Hardware | RAM | Model | Download size |
|---|---|---|---|
| Pi 5 (8GB) | 8GB | `llama3.1:8b` | ~5GB |
| Pi 5 (4GB) / Pi 4 (4GB+) | 4GB+ | `llama3.2:3b` | ~2GB |
| Pi 4 (2GB) / Pi 3 | 2GB | `phi3:mini` | ~2GB |
| Pi 3 (1GB) | 1GB | `tinyllama` | ~600MB |

The first boot script selects automatically based on your Pi's RAM.

To change the model later:
```bash
# On the Pi:
ollama pull llama3.2:3b
# Edit /opt/famclaw/config.yaml → llm.model
sudo systemctl restart famclaw
```

---

## Troubleshooting

**Can't reach famclaw.local:**
- mDNS can take a minute to propagate
- Try the IP address directly
- On Windows, install [Bonjour](https://support.apple.com/kb/DL999)

**First boot taking too long:**
- Model download can take 10–30 min on slow connections
- Check progress: `ssh pi@famclaw.local "sudo journalctl -u famclaw-firstboot -f"`

**FamClaw not starting:**
- Check logs: `sudo journalctl -u famclaw -f`
- Verify Ollama is running: `systemctl status ollama`

**Out of disk space:**
- Use a larger SD card (32GB+)
- Large models (8B+) need significant space
