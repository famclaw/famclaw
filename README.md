# 🛡️ FamClaw

**A secure, local-first family AI gateway. Runs on Raspberry Pi, Mac, or an old Android phone.**

FamClaw is a lightweight Go gateway that connects your family to any AI model — local or cloud — through Telegram, WhatsApp, Discord, and a web interface. Every message goes through a policy engine before the AI ever sees it.

---

## What it is

- **A gateway, not an AI.** FamClaw routes messages between your family and whatever LLM you configure — Ollama on your home server, OpenAI, Anthropic, OpenRouter, or any OpenAI-compatible endpoint.
- **A policy enforcer.** Every message is evaluated by OPA (Open Policy Agent) before reaching the LLM. Kids get age-appropriate responses. Sensitive topics require parental approval.
- **A family assistant.** Age-aware profiles, parental approval workflow, notification to parents via email/SMS/Slack/Discord/ntfy.

---

## How it works

```
Family member sends message
  → via Telegram / WhatsApp / Discord / Web UI
    → FamClaw identifies user from gateway account
      → OPA policy evaluates: allow / block / request approval
        → if allow: forwards to your LLM endpoint
          → streams response back
```

FamClaw itself uses ~20MB RAM. The LLM runs elsewhere — on a Mac Mini on your LAN, a cloud API, or any OpenAI-compatible server.

---

## Hardware

| Device | Role |
|--------|------|
| Raspberry Pi 3/4/5 | Run FamClaw 24/7, flash SD card and plug in |
| Mac Mini | Run as background daemon |
| Old Android phone | Run via the FamClaw Android app, plug into charger |
| Any Linux box | One binary, no dependencies |

---

## LLM backends

FamClaw talks to any OpenAI-compatible endpoint:

```yaml
llm:
  primary:
    base_url: "http://192.168.1.10:11434"  # Ollama on your Mac Mini
    model: "llama3.2:3b"

  fallbacks:
    - base_url: "https://api.openai.com/v1"
      model: "gpt-4o-mini"
      api_key: "${OPENAI_API_KEY}"
```

---

## Quick start

### Raspberry Pi (flash and plug in)
```bash
# Flash famclaw-rpi4-arm64.img.xz to SD card with Raspberry Pi Imager
# Plug in, wait 2 minutes, open:
http://famclaw.local:8080
```

### Mac / Linux
```bash
curl -fsSL https://github.com/famclaw/famclaw/releases/latest/download/install.sh | bash
```

### Build from source
```bash
git clone https://github.com/famclaw/famclaw
cd famclaw
make build
./bin/famclaw --config config.yaml
```

---

## Messaging gateways

| Gateway | Status |
|---------|--------|
| Web UI | Built — HTTP + WebSocket + embedded UI |
| Telegram | Built — long-poll Bot API |
| Discord | Built — via discordgo |
| WhatsApp | Placeholder — needs whatsmeow QR pairing |

Each family member's gateway account maps to their profile. Emma's Telegram account → Emma's age policy. Parent's Discord account → parent access.

---

## Policy system

Policies are [OPA Rego](https://www.openpolicyagent.org/) files. Edit them, test them with `opa test`, or ask an LLM to write new rules in plain English.

Three tiers per age group:

```
allow        → goes straight to LLM
request_approval → parent gets notified, child waits
block        → never reaches LLM
```

Default age groups: `under_8`, `age_8_12`, `age_13_17`, `parent`.

---

## Skills

FamClaw uses the [AgentSkills](https://docs.openclaw.ai/tools/skills) spec — the same `SKILL.md` format used by OpenClaw and PicoClaw. Skills from [famclaw/skills](https://github.com/famclaw/skills) work in all three runtimes.

```bash
famclaw skill install seccheck
```

---

## SecCheck

Before installing any skill, scan it:

```bash
famclaw --seccheck https://github.com/someone/some-skill
```

Checks for hardcoded secrets, suspicious network calls, CVEs via osv.dev, typosquatting, and runs in a sandbox.

---

## Status

🚧 **v0.3.0-beta — agent core rewrite complete.**

### What works

| Feature | Status |
|---------|--------|
| **Policy gate** | OPA rules for input, tool calls, and output (33 Rego tests) |
| **Pipeline engine** | Composable stages: classify → policy → LLM → tools → output filter |
| **Multi-backend LLM** | OpenAI-compatible: Ollama, llama.cpp, Groq, OpenAI, OpenRouter |
| **Smart tool selection** | Token-budget-aware filtering, role+skill scoping |
| **Context compression** | Tiered truncation keeping system prompt + pinned messages |
| **Subagent dispatching** | Concurrent subagents with explicit LLM profile control |
| **Skill adapters** | FamClaw (SKILL.md), OpenClaw (SOUL.md), Claude Code (.md) |
| **llama.cpp sidecar** | Spawns llama-server, GGUF model catalog, TurboQuant support |
| **Security scanning** | Honeybadger runtime stage, install-time + stale scan gates |
| **Web UI** | Chat, parent dashboard, 5-step wizard with AI profiles |
| **Telegram + Discord** | Fully wired gateway bots |
| **MCP tools** | Multi-transport (stdio/HTTP/SSE), unified tool registry |
| **LLM profiles** | Multiple named endpoints, per-user assignment via wizard |
| **CI/CD** | CodeQL, govulncheck, SBOM, cosign signing, TruffleHog |

### Recommended models

| Hardware | Model | Why |
|----------|-------|-----|
| Mac Mini M1+ 16GB | `gemma4:e4b` | Native tool calling, multimodal |
| RPi 5 8GB | `gemma4:e2b` | Fits in 3GB Q4, tool calling |
| RPi 4 4GB | `qwen3:4b` | Best efficiency |
| RPi 3 / Android | Use remote | Gateway only |

See [docs/BACKENDS.md](docs/BACKENDS.md) for inference engine comparison.

See [AGENTS.md](./AGENTS.md) for the full build plan.

---

## License

[AGPL-3.0](./LICENSE) — free for personal and family use. Contact us for commercial licensing.