# Troubleshooting

## FamClaw won't start

**What happened:** The binary exits immediately or the service fails.

**What to check:**
1. Is the config file found? FamClaw looks in: `./config.yaml` → `~/.famclaw/config.yaml` → `/opt/famclaw/config.yaml`
2. Is the server secret set? If empty, FamClaw generates one automatically
3. Check logs: `journalctl -u famclaw -f` (Linux) or `tail -f ~/logs/famclaw.log` (Mac)

**Common causes:**
- Port 8080 already in use → change `server.port` in config
- Database directory doesn't exist → FamClaw creates it automatically, but check permissions

---

## Can't reach famclaw.local

**What happened:** Browser shows "site can't be reached."

**What to do:**
1. Wait 1-2 minutes after boot — mDNS needs time to advertise
2. Try the IP address directly: find it with `hostname -I` on the device
3. On Windows: mDNS may need Bonjour installed
4. On Android: mDNS is unreliable — use the IP address

**Time to fix:** About 1 minute.

---

## AI is not responding

**What happened:** You send a message but get no response, or see an error.

**What to check:**
1. Is your AI provider running? Dashboard shows connection status
2. If using Ollama on LAN: is the device powered on? Is Ollama running?
3. If using a cloud provider: is your API key valid? Is the service up?

**What to do:**
- Open Settings in the dashboard
- Click "Test connection" next to your AI provider
- If it fails: check the URL and API key

**Time to fix:** About 2 minutes.

---

## Child sees "waiting for parent" but parent didn't get a notification

**What happened:** The approval notification didn't reach you.

**What to check:**
1. Are notifications configured? Open Settings → Notifications
2. If using Telegram: is the bot still connected?
3. Check the dashboard directly — pending approvals are always shown there

**Quick fix:** Open the dashboard and approve/deny directly. No notification needed.

---

## Messages are slow

**What happened:** Responses take more than 10 seconds.

**Possible causes:**
- Local AI model too large for your hardware → try a smaller model in Settings
- Network issues to cloud provider → check your internet connection
- Multiple family members chatting simultaneously → this is normal, each gets their own queue

**Recommended models by hardware:**
- RPi 5 8GB: `gemma4:e2b`
- RPi 4 4GB: `qwen3:4b`
- Mac Mini: `gemma4:e4b`

---

## How to update FamClaw

Run:
```bash
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/update.sh | bash
```

This downloads the latest version, verifies it, and installs it. If the new version fails to start, it automatically rolls back to the previous version.

---

## How to reset everything

If you want to start fresh:
1. Stop FamClaw: `sudo systemctl stop famclaw`
2. Delete the database: `rm ~/.famclaw/data/famclaw.db`
3. Delete the config: `rm ~/.famclaw/config.yaml`
4. Start FamClaw: `sudo systemctl start famclaw`
5. The setup wizard will appear again
