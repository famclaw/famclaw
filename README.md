# 🛡️ FamClaw

**A secure, local-first family AI gateway. Runs on Raspberry Pi, Mac, or any Linux box.**

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
# Plug in, wait 2 minutes, find the device IP from your router and open:
http://<your-pi-ip>:8080
```

> mDNS (`famclaw.local`) was removed in v0.5.x because it didn't resolve
> reliably on Windows or many home routers. Use the device's IP address
> from your router's DHCP leases page or `ip addr` on the Pi.

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

Policies are [OPA Rego](https://www.openpolicyagent.org/) files. The default rule set lives at `internal/policy/policies/` and is **embedded in the binary** via `go:embed` — a downloaded release runs without any external policy directory. To override with custom rules, set `policies.dir` (and `policies.data_dir`) in `config.yaml` to a directory of your own `.rego` and JSON files. Run `opa test internal/policy/policies/family/ internal/policy/policies/data/ -v` to test the built-in rules locally.

Three tiers per age group:

```
allow        → goes straight to LLM
request_approval → parent gets notified, child waits
block        → never reaches LLM
```

Default age groups: `under_8`, `age_8_12`, `age_13_17`, `parent`.

---

## Skills

FamClaw uses the [AgentSkills](https://docs.openclaw.ai/tools/skills) spec — the same `SKILL.md` format used by OpenClaw, PicoClaw, and NanoBot. Skills from [famclaw/skills](https://github.com/famclaw/skills) work in all four runtimes. [HoneyBadger](https://github.com/famclaw/honeybadger) scans every skill before installation.

```bash
famclaw skill install seccheck
```

---

## Agent dispatch (`spawn_agent`)

The parent LLM can delegate sub-tasks to a different LLM profile via a built-in tool. Use it to send research-style or compute-heavy work to a local model (e.g., Qwen3-14B on Ollama) while the parent stays on a fast/cloud model.

```jsonc
// Tool call from the parent LLM:
{
  "name": "builtin__spawn_agent",
  "arguments": {
    "prompt": "Summarize the key risks in the attached log",
    "profile": "qwen3-local",          // optional: omit to use the default profile
    "timeout_seconds": 120,             // default 300, capped at 1800
    "tools": ["fs.read", "web.search"], // allowlist; omit for NO MCP tools (default-deny)
    "deny_tools": ["fs.write"]          // subtracted from the allowlist
  }
}
```

Concurrency is bounded by the scheduler (`subagent.NewScheduler(2)` in `cmd/famclaw/main.go`). Each `spawn_agent` invocation gets a dedicated result channel — concurrent calls do not cross-deliver. The tool is parent-only (role-gated via `turn.Tools`) and has no MCP tool access unless the parent explicitly allowlists. Lives in `internal/subagent/`.

---

## Web fetch (`web_fetch`)

Off by default. When enabled, the LLM gets a `web_fetch` tool that retrieves a URL and returns extracted text — `text/html` is parsed via `golang.org/x/net/html` and stripped of `<script>`/`<style>`/`<head>`; `text/plain` and `application/json` pass through. Useful for "what's the weather", "look up the docs page for X", and similar fetches.

Enable in `config.yaml`:

```yaml
tools:
  web_fetch:
    enabled: true
    allowed_roles: [parent]   # role gate — checked when registering the tool
    url_allowlist:            # empty = any host; subdomains of an allowed host match
      - wikipedia.org
      - en.wikipedia.org
    max_bytes: 262144         # 256 KB response cap
    timeout_seconds: 15
```

Defense in depth:

- **Role gate** at registration — the tool is only added to the LLM's tool list for users in `allowed_roles`.
- **OPA `tool_policy` rule** at the tool loop — `parent` and `age_13_17` are allowed; `under_8` and `age_8_12` are denied. Blocked calls never dispatch.
- **URL allowlist** in `handleWebFetch` — only `http`/`https` schemes; the request host must equal an allowlist entry or be a subdomain of one. Empty allowlist permits any host.
- **Size + timeout caps** in `internal/webfetch` — `MaxBytes` enforced via `io.LimitReader`, redirect chain capped at 5 hops, request `Timeout` from config.

The fetcher itself is in `internal/webfetch/`; the agent handler lives in `internal/agent/agent.go` (`handleWebFetch`).

---

## Security scanning

FamClaw uses [HoneyBadger](https://github.com/famclaw/honeybadger) to scan skills at two points:

**Install time.** `famclaw skill install <path>` scans with HoneyBadger before writing anything to disk. FAIL verdicts block the install by default.

**Runtime, asynchronously.** Tools used during a conversation are scanned in the background after the turn completes. If a scan fails, the tool is quarantined and filtered out of the next turn. This never adds latency — scanning runs in parallel with or after the response.

All behavior is configurable in `config.yaml` under the `seccheck:` section.

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
| **Agent dispatch** | `spawn_agent` builtin tool — parent LLM delegates to a different profile (default-deny MCP tools, per-call timeout, scheduled with concurrency cap) |
| **Web fetch** | `web_fetch` builtin tool (off by default) — fetch a URL and return extracted text, role-gated + OPA `tool_policy` + per-host allowlist + size/timeout caps |
| **Skill adapters** | FamClaw (SKILL.md), OpenClaw (SOUL.md), Claude Code (.md) |
| **llama.cpp sidecar** | Spawns llama-server, GGUF model catalog, TurboQuant support |
| **Security scanning** | Honeybadger runtime stage, install-time + stale scan gates |
| **Web UI** | Chat, parent dashboard, 5-step wizard with AI profiles, PIN-gated skill install/remove |
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
| RPi 3 | Use remote | Gateway only |

See [docs/BACKENDS.md](docs/BACKENDS.md) for inference engine comparison.

See [AGENTS.md](./AGENTS.md) for the full build plan.

---

## License

[AGPL-3.0](./LICENSE)
