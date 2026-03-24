# 🛡️ FamClaw

**A family AI assistant built for privacy, security, and local-first deployment.**

FamClaw is a ground-up Go rewrite of the OpenClaw agent architecture, purpose-built as a family assistant that runs entirely on your local network — no cloud, no subscriptions, no data leaving your home.

---

## What it does

- **Family chat interface** — each family member has a profile. Kids get age-appropriate responses, parents get full access.
- **OPA policy engine** — every query is evaluated by Open Policy Agent before reaching the LLM. Topics are allowed, blocked, or sent to parents for approval.
- **Parental approval workflow** — kids asking about age-restricted topics sends a notification to parents via email/SMS/Slack/Discord/ntfy. One-click approve or deny from any device.
- **SecCheck** — security scanner for OpenClaw skills/MCP repos before you install them. Static analysis + CVE checks via osv.dev + sandbox execution.
- **Runs everywhere locally** — RPi 3/4/5, old Android phones via Termux, Mac Mini, any Linux box.

---

## Architecture

```
famclaw/
├── cmd/famclaw/          # Single binary entry point
├── internal/
│   ├── agent/            # Conversation loop + LLM streaming
│   ├── classifier/       # Query → topic category (no LLM needed)
│   ├── config/           # YAML config + env var expansion
│   ├── llm/              # Ollama client (streaming)
│   ├── mdns/             # LAN discovery (famclaw.local)
│   ├── notify/           # Email, Slack, Discord, SMS, ntfy
│   ├── policy/           # OPA evaluator (embedded)
│   ├── seccheck/         # Security scanner
│   ├── store/            # SQLite (pure Go, no CGO)
│   └── web/              # HTTP + WebSocket server + embedded UI
├── policies/
│   ├── family/           # OPA Rego policies
│   └── data/             # Topic taxonomy JSON
├── scripts/
│   ├── install-rpi.sh    # One-command RPi installer
│   └── install-termux.sh # Android (Termux) installer
└── Makefile              # Cross-compile for all targets
```

---

## Quick start

### Mac Mini / Linux
```bash
git clone https://github.com/famclaw/famclaw
cd famclaw
cp .env.example .env && nano .env   # Add notification credentials
make build
make run
# Open http://localhost:8080
```

### Raspberry Pi (fresh install)
```bash
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-rpi.sh | bash
# Open http://famclaw.local:8080 from any device on your network
```

### Old Android phone (Termux)
```bash
# 1. Install Termux from F-Droid (not Play Store)
# 2. In Termux:
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/install-termux.sh | bash
famclaw-start
```

---

## LLM model recommendations

| Hardware | RAM | Recommended model |
|---|---|---|
| RPi 5 | 8GB | `llama3.1:8b` |
| RPi 5 / Mac Mini | 4–8GB | `llama3.2:3b` |
| RPi 4 | 4GB | `phi3:mini` |
| RPi 3 / old phone | 2GB | `tinyllama` |

All models run locally via [Ollama](https://ollama.com).

---

## SecCheck

Run a security scan on any OpenClaw skill or MCP repo before installing:

```bash
famclaw --seccheck https://github.com/some-user/some-skill
```

**Scans for:**
- Hardcoded secrets & API keys (30+ patterns)
- Network exfiltration patterns
- Typosquatting (dependency names vs 50+ popular packages)
- CVEs via [osv.dev](https://osv.dev) (free, no key needed)
- Data access vs SKILL.md declaration mismatch
- Runtime behavior in Docker sandbox (falls back to macOS `sandbox-exec`)

---

## Cross-compilation

```bash
make cross          # All platforms
make cross-rpi4     # RPi 4/5 (arm64)
make cross-rpi3     # RPi 2/3/Zero (armv7)
make cross-android  # Android arm64 + armv7
make cross-mac-arm  # Mac Apple Silicon
make cross-linux64  # Linux x86_64

# Deploy directly to RPi over SSH
RPI_HOST=pi@192.168.1.50 make install-rpi
```

All binaries are CGO-free — no C toolchain needed for cross-compilation.

---

## Notification channels

All configurable in `config.yaml`, all independent:

| Channel | Use case |
|---|---|
| Email (SMTP) | Full approval email with one-click buttons |
| Slack | Team/family Slack workspace |
| Discord | Family Discord server |
| SMS (Twilio) | Any phone, no app needed |
| ntfy | Self-hosted push, zero cloud dependency |

---

## Policy system

Policies are written in [OPA Rego](https://www.openpolicyagent.org/docs/latest/policy-language/). The decision tree:

1. **Parent** → always allow
2. **Hard blocked** → always deny (adult content, weapons, etc.)
3. **Previously approved** → allow
4. **Age-appropriate topic** → allow
5. **Needs approval** → notify parents, queue request
6. **Already pending** → tell child to wait

Customize `policies/family/decision.rego` and `policies/data/topics.json` to fit your family.

---

## OpenClaw skill compatibility

FamClaw can read `SKILL.md` files from OpenClaw skills. Import a skill:

```bash
famclaw skill install https://github.com/some-user/some-openclaw-skill
# Always runs seccheck first (configurable)
```

---

## License

MIT — fork it, run it, own it. Your data stays yours.
