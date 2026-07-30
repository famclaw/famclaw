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
   - **Hostname:** `famclaw`
   - **WiFi:** enter your network name and password
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
2. Power on
3. Wait 1–2 minutes — FamClaw starts automatically

---

## Step 4 — Open FamClaw

Once the first boot completes, open on **any device on your home network**:

```
http://<your-pi-ip>:8080
```

Find the IP address from your router's DHCP leases page, or from the Pi itself:
```bash
hostname -I
```

> mDNS (`famclaw.local`) was removed in v0.5.x because it didn't resolve reliably
> on Windows or many home routers. Use the device's IP address instead.

---

## Step 5 — Configure via web UI

The first-boot wizard appears automatically. Set up:

1. **LLM endpoint** — where your AI runs

| Backend | URL |
|---------|-----|
| Ollama on LAN | `http://192.168.1.10:11434` |
| OpenAI | `https://api.openai.com/v1` |
| Anthropic | `https://api.anthropic.com/v1` |
| OpenRouter | `https://openrouter.ai/api/v1` |

2. **Family members** — name, age group, parent PIN
3. **Gateways** — Telegram/Discord tokens (optional)

That's it. No terminal, no SSH, no config files.

---

## Troubleshooting

**Can't reach the device:**
- Verify the IP address from `hostname -I`
- Ensure the Pi and your device are on the same network
- Check that port 8080 isn't blocked by a firewall

**FamClaw not starting:**
- Check logs: `sudo journalctl -u famclaw -f`
- Verify LLM endpoint is reachable from the Pi

**Out of disk space:**
- Use a larger SD card (32GB+)
- Large models (8B+) need significant space
