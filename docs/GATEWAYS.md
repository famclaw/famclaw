# Gateway Setup

FamClaw connects to Telegram, Discord, and WhatsApp so your family can chat with the AI from their existing messaging apps.

## Telegram

### 1. Create a bot

1. Open Telegram, search for `@BotFather`
2. Send `/newbot`
3. Choose a name (e.g., "FamClaw Family AI")
4. Choose a username (e.g., `famclaw_family_bot`)
5. Copy the **bot token**

### 2. Add to config

```yaml
gateways:
  telegram:
    enabled: true
    token: "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ"
```

### 3. Link family accounts

Each family member messages the bot once. Then link their Telegram user ID to their FamClaw profile in the web dashboard.

To find a Telegram user ID: have them message the bot, then check the FamClaw logs:
```bash
sudo journalctl -u famclaw | grep "unknown account"
```

---

## Discord

### 1. Create a bot

1. Go to [Discord Developer Portal](https://discord.com/developers/applications)
2. Click **New Application** → name it "FamClaw"
3. Go to **Bot** → click **Add Bot**
4. Enable **Message Content Intent** under Privileged Gateway Intents
5. Copy the **bot token**

### 2. Invite the bot

Go to **OAuth2 → URL Generator**:
- Scopes: `bot`
- Bot Permissions: `Send Messages`, `Read Message History`

Copy the URL, open it, and add the bot to your family's Discord server.

### 3. Add to config

```yaml
gateways:
  discord:
    enabled: true
    token: "your-discord-bot-token"
```

### 4. Link accounts

Same as Telegram — each family member sends a message in the Discord server. The bot logs unknown accounts. Link them in the web dashboard.

---

## WhatsApp

> **Status:** WhatsApp gateway is a **placeholder** — the whatsmeow integration is not yet implemented. The config and code structure are ready, but the QR pairing flow and message handling are not functional yet.

Once implemented, WhatsApp will use whatsmeow (pure Go, no Chromium):

```yaml
gateways:
  whatsapp:
    enabled: true
    db_path: "/opt/famclaw/whatsapp.db"
```

---

## How identity works

Every gateway message goes through identity resolution:

```
Telegram msg from user 12345
  → identity.Resolve("telegram", "12345")
  → returns "emma" (or nil if unknown)
  → unknown accounts get onboarding message
  → known accounts get policy-checked AI response
```

The same family member can be linked to multiple gateways. Emma's Telegram, Discord, and WhatsApp accounts all map to her FamClaw profile with her age-based policy.

---

## Security notes

- Bot tokens are secrets — never commit them to git
- Use environment variables or the `config.yaml` (not checked into source control)
- The config file should be readable only by the famclaw user: `chmod 600 /opt/famclaw/config.yaml`
