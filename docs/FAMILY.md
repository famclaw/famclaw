# Guide for Parents

FamClaw is a private AI assistant for your family. This guide explains how it works — no technical knowledge needed.

## How it works

Your family talks to FamClaw through the web browser, Telegram, or Discord. Every message goes through family rules before the AI responds.

- **Young children (under 8):** Only safe topics like homework, stories, and nature
- **Kids (8-12):** More topics allowed, but some need your permission first
- **Teens (13-17):** Most topics allowed, but sensitive ones still need your OK
- **Parents:** Everything is allowed. You manage the family rules.

## What happens when a child asks about something sensitive

1. FamClaw holds the response and sends you a notification
2. You see the request in your dashboard — the child's name, what they asked, and the topic
3. You tap **Approve** or **Deny**
4. If approved, FamClaw answers the child's question
5. If denied, the child sees a friendly "your parent said no" message

## The parent dashboard

Open FamClaw in your browser and select your name. Enter your PIN to access the dashboard.

You'll see:
- **Pending approvals** — requests waiting for your decision
- **Recent conversations** — what each child has been asking
- **Family members** — who's set up and their age group
- **Settings** — change the AI provider, add gateways, manage notifications

## Adding family members

1. Open the dashboard
2. Go to Settings (gear icon)
3. Add a person — enter their name and age group
4. Save

## Linking messaging apps

If your family uses Telegram or Discord, FamClaw can chat there too.

1. Open Settings → Gateways
2. Follow the guide for your messaging app
3. Each family member messages the bot once
4. You link their account to their FamClaw profile

## What FamClaw remembers about your family

FamClaw keeps two kinds of information so it can help without you repeating things every time.

**Family facts** are shared household details — allergies, dietary restrictions, important dates, pets, and any custom categories you add. FamClaw automatically shares safety-critical facts with the AI on every message — by default, this includes **allergies** and **dietary restrictions** — so the assistant always knows, for example, that someone is allergic to nuts before it suggests a recipe. You manage these in the dashboard under Family State. Other facts, like birthdays and pets, stay in FamClaw but are only looked up when a conversation actually needs them.

**User memories** are personal notes your family members ask FamClaw to remember — a nickname, a fear of dogs, a friend's birthday. Each family member's memories are shared with the AI in their own conversations, so FamClaw always remembers what you've told it. You can also ask FamClaw to find a specific memory by keyword — it searches across the label, value, and category of every memory to find what's relevant.

Kids proposing a new family fact go through the usual approval flow; parents can add, update, or delete directly in the dashboard.

## Proactive reminders

FamClaw can remind family members about things at the right time — it doesn't wait for the next message. When a reminder's time comes, FamClaw sends it straight to that family member's messaging app.

Say something natural like:
- "Remind me to take out the trash in 2 hours"
- "Remind me tomorrow at 9:00 am to call Grandma"
- "Remind me at 14:30 to pick up Emma from soccer"
- "Remind me 2 days from now at 5:00 pm to prep for Sunday's show"

The assistant parses the time (relative like "in 2 hours" or "30m", absolute like "tomorrow 9:00 am" / "monday 10:00", or a day offset with an explicit time like "in 2 days at 17:00", which is honored exactly on that calendar day) and the message, then stores the reminder. A bare "in N days" reminds at the current time N days from now — to pin a specific time of day, include it. FamClaw's scheduler delivers due reminders proactively through the recipient's most recently used messaging app.

### Reminding another family member

A parent can set a reminder for someone else in the family:
- "Remind Emma in 10 minutes to come downstairs for dinner"

Only parents can set reminders for other people — children can remind themselves but not others. If the target hasn't messaged FamClaw yet on any gateway, FamClaw tells you it doesn't know how to reach them yet rather than guessing a channel. The feature is powered by the `builtin__add_reminder` tool.

## The assistant can message another family member

On its own initiative — not just in reply to one person — FamClaw can send a message to another family member on their messaging platform. You'll see this, for example, when a reminder fires for someone else, or when the assistant decides a note belongs to a different user.

- **Who can be messaged:** only the family members configured in your Settings → Family. FamClaw will not address anyone outside the family.
- **How it reaches them:** via the recipient's most recently connected app (Telegram or Discord). If a family member hasn't messaged FamClaw yet, FamClaw tells you it doesn't know how to reach them — it never guesses a channel.
- **It's parent-gated:** only a parent's assistant can initiate one of these messages.
- **It's audited.** Every proactive message is recorded in the audit log with who sent it, who it was for, and what it said, and it also appears in the recipient's conversation history in your dashboard.

The feature is powered by the `builtin__send_message` tool.

## The assistant can send you files

When the assistant makes a file for you — a document, an image, anything it writes to the conversation's workspace — it can attach and send it right back into your chat.

- **Same conversation only.** The file goes to the channel or DM you're talking in. There is no way for the assistant to send a file to a different person (that's what the parent-gated `send_message` tool is for, and it stays text).
- **What it may send.** Only files that live in your conversation's workspace — the same place `file_read` and friends are confined to. Files outside that area cannot be addressed, and anything over 25 MB (Discord's bot limit) is refused with a clear message.
- **Audited.** Every sent file is recorded in the audit log — what was sent, to whom, and its size. The file's content is not duplicated into the log.
- **Honest failures.** If the platform can't take a file (for example, Telegram doesn't support it yet), the assistant says so instead of pretending it delivered.

The feature is powered by the `builtin__send_file` tool.

## Topics that are always blocked

Some topics are never allowed, even with your approval:
- Adult content
- Self-harm
- Hate speech
- Instructions for illegal activities

These are hard-coded into the family rules and cannot be overridden.

## If FamClaw isn't responding

1. Check that your AI provider is running (the dashboard shows connection status)
2. If using a local AI (Ollama), make sure the device it runs on is powered on
3. Restart FamClaw: the web UI has a restart option in Settings

## Privacy

- All conversations stay on your home network
- Nothing is sent to the cloud (unless you chose a cloud AI provider)
- You can see everything your children ask in the dashboard
- No ads, no tracking, no data collection

## Web Tools

FamClaw provides built-in web tools that can be used by family members:

### Web Fetch (`web_fetch`)

The `web_fetch` tool allows users to fetch content from websites. It extracts HTML→text, plain/JSON, and PDF text layers. Hosts are gated by a URL policy: in `allowlist` mode a non-empty `url_allowlist` is required (an empty list denies all — the legacy SSRF guard, unchanged in allowlist mode), while in the default `denylist` mode public hosts are allowed and only `url_denylist` entries are blocked (see "URL Policy Configuration" below). Its optional headless-browser fallback (`fallback_to_browser`) fires for thin JS-heavy pages and on 403/429 bot-blocks, and requires `tools.browser.enabled` plus a reachable browser pool.

### Web Search (`web_search`)

The `web_search` tool allows users to search the web for current information (news, weather, prices, flights, products, public docs). It returns a list of title/url/snippet results. It queries SearXNG (default — requires `tools.web_fetch.enabled=true` and reuses its `url_allowlist` as the host gate) or hosted Tavily (`provider: tavily` + the `FAMCLAW_WEB_SEARCH_API_KEY` env var).

### URL Policy Configuration

The `tools.web_fetch` section supports two URL policy modes:

1. **Allowlist mode** (`url_policy: allowlist`): Only hosts in `url_allowlist` are permitted. 
   - Requires at least one host in `url_allowlist`
   - Empty list denies all (SSRF guard)
   - Hosts are matched exactly or as subdomains (e.g., "example.com" matches "www.example.com")

2. **Denylist mode** (`url_policy: denylist`): All public hosts are allowed by default, with explicit blocks in `url_denylist`.
   - Includes a bundled kid-safe baseline (Aegis Blocklist) that blocks adult content, hate groups, and harmful sites
   - Parent-maintained denylist entries in `url_denylist` (exact or subdomain matches)
   - Hosts are matched exactly or as subdomains
   - Suspicious site detector (W4) that can block sites based on reputation

The default mode is `denylist` when `url_allowlist` is empty, providing a secure-by-default experience.

**Configuration example:**
```yaml
tools:
  web_fetch:
    enabled: true
    url_policy: "denylist"
    url_denylist:
      - "adult.example.com"
      - "hate.example.com"
    max_bytes: 262144
    timeout_seconds: 30
    allowed_roles:
      - "parent"
      - "child"
  web_search:
    enabled: true
    provider: "searxng"
    endpoint: "http://localhost:8888"
```

The `url_policy` field accepts "allowlist" or "denylist" values. When unset, the mode is determined by the presence of an `url_allowlist`:
- Non-empty allowlist → `allowlist` mode (backwards compatible)
- Empty/missing allowlist → `denylist` mode (secure-by-default)

### Admin Tools

Parent admins can manage web fetch settings via:
- Allowlist: managed conversationally via the LLM admin tools `builtin__allowlist_list` / `builtin__allowlist_add` / `builtin__allowlist_remove` (internal/agent/tools/admin/).
- Denylist: managed conversationally via the LLM admin tools `builtin__denylist_list` / `builtin__denylist_add` / `builtin__denylist_remove` (internal/agent/tools/admin/).
- Both lists persist in config.yaml under `tools.web_fetch.url_allowlist` / `url_denylist` and can be read/updated via the `/api/settings` GET/POST endpoint.

These are only available to users with the "parent" role.
