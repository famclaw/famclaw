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
- [Agent Setup Guide](./docs/AGENT_SETUP.md) — Complete setup instructions for AI coding agents

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

| Platform | Backend | api_key needed |
|---|---|---|
| RPi 3/4/5 | Ollama (local, auto-installed by firstboot.sh) | No |
| Mac Mini | Ollama (local) | No |
| Old Android (Termux) | OpenAI / Anthropic / OpenRouter / another device's Ollama | Yes (or LAN URL) |
| Any device | Can point at RPi's Ollama on LAN | No |
| Any device | Claude CLI (`provider: claude_cli`) | No (uses local claude binary) |

```yaml
llm:
  base_url: "http://192.168.1.10:11434"  # Ollama on your Mac Mini
  model: "llama3.2:3b"
  # Per-call LLM request timeout in seconds. Each chat/tool call gets its
  # own context deadline. Default: 300 (5 minutes).
  timeout_seconds: 300

  profiles:
    cloud:
      base_url: "https://api.openai.com/v1"
      model: "gpt-4o-mini"
      api_key: "${OPENAI_API_KEY}"

  # When a message carries an image attachment (a photo sent on Telegram
  # or Discord), FamClaw routes it to this LLM profile instead of the
  # normal per-user model — text-only messages always use the normal
  # endpoint. Set this to a vision-capable model (e.g. qwen2.5-vl,
  # llama3.2-vision, gemma3). When empty, the per-user endpoint is used
  # for both image and text (handy if your main model is itself vision-
  # capable). Images are sent to the configured LLM endpoint, which may be
  # remote — they stay on-device only when that endpoint is local.
  vision_profile: ""
```

**Security note:** `llm.api_key` is loaded from plaintext YAML by default.
Set `FAMCLAW_LLM_API_KEY` environment variable to override — it takes precedence and avoids logging the plaintext warning.

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

### First-party Skills

#### `family-knowledge` — family memory and facts
A first-party skill that gives the LLM access to a persistent family knowledge base for storing and retrieving household facts (members, allergies, dietary rules, doctors, schedules, house rules, pets, important dates). The knowledge is shared across all family members and conversations.

The skill provides tools for reading facts, proposing new facts (children must get parent approval), and mutating facts (parents can add, update, or delete facts directly). Parents can also define custom categories for organizing knowledge.

The built-in categories `allergies` and `dietary_restrictions` are always injected into every system prompt for safety-critical information. Other categories are accessed on-demand via `get_family_state`.

See [`skills/family-knowledge/SKILL.md`](skills/family-knowledge/SKILL.md) for full documentation.

#### `image-understanding` — image analysis and description
A first-party skill that enables FamClaw to understand and analyze images provided by users. When a user shares an image (or references one), FamClaw can describe what's in it, read text contained within the image, and answer questions about the visual content.

See [`skills/image-understanding/SKILL.md`](skills/image-understanding/SKILL.md) for full documentation.

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

Research subagents run asynchronously: the parent acknowledges the task immediately ("Started your research (task N). I'll post the result here when it's done.") and, when the subagent finishes, posts its result back into the originating conversation on the gateway it came from (Telegram, Discord, web). Delivery is bounded by a 30-second timeout per send.

---

## Web fetch (`web_fetch`)

Off by default. When enabled, the LLM gets a `web_fetch` tool that retrieves a URL and returns extracted text — `text/html` is parsed via `golang.org/x/net/html` and stripped of `<script>`/`<style>`/`<head>`; `text/plain` and `application/json` pass through. Useful for "what's the weather", "look up the docs page for X", and similar fetches.

Enable in `config.yaml`:

```yaml
tools:
  web_fetch:
    enabled: true
    allowed_roles: [parent]   # role gate — checked when registering the tool
    url_allowlist:            # REQUIRED — empty list denies all (SSRF guard).
      - wikipedia.org         #   Subdomains of an allowed host match automatically.
      - en.wikipedia.org
    max_bytes: 262144         # 256 KB response cap
    timeout_seconds: 15
    fallback_to_browser: false  # opt-in: fall back to headless browser for JS-heavy sites; requires tools.browser.enabled
    fallback_min_text_length: 10 # below this many chars of extracted text, attempt the browser fallback
```

Defense in depth:

- **Role gate** at registration — the tool is only added to the LLM's tool list for users in `allowed_roles`.
- **OPA `tool_policy` rule** at the tool loop — `parent` and `age_13_17` are allowed; `under_8` and `age_8_12` are denied. Blocked calls never dispatch.
- **URL allowlist** in `handleWebFetch` — only `http`/`https` schemes; the request host must equal an allowlist entry or be a subdomain of one. **An empty allowlist denies all fetches** (SSRF guard) — operators must list the hosts they trust. The same predicate is re-applied to every redirect target inside `webfetch.Fetch`.
- **Private-network access** — By default FamClaw *allows* private-network access (it is a home-LAN product that must reach co-located SearXNG, Playwright, llama-server). The host allowlist still applies regardless. To block loopback/RFC1918/RFC4193 ULA at the dialer, set `tools.web_fetch.block_private_networks: true`.
- **Size + timeout caps** in `internal/webfetch` — `MaxBytes` enforced via `io.LimitReader`, redirect chain capped at 5 hops, request `Timeout` from config.

The fetcher itself is in `internal/webfetch/`; the agent handler lives in `internal/agent/agent.go` (`handleWebFetch`).

For JS-heavy sites where HTML→text extraction yields too little content, `web_fetch` can fall back to a headless browser (the built-in `browser` tool), reusing the same host allowlist. This fallback is **off by default** (`tools.web_fetch.fallback_to_browser: false`); enable it only alongside `tools.browser.enabled`. When enabled and the plain fetch returns fewer than `fallback_min_text_length` chars, the browser navigates and extracts rendered text. Every failure path returns a distinct, honest error — a nil/unavailable browser pool, a browser fetch failure, or an empty rendered result — so an empty page is never silently returned as a successful fetch. Integration tests for the live browser path live behind the `integration` build tag (`go test -tags integration ./internal/agent/ -run TestFetchWithBrowser_Integration`), skipped cleanly where no Playwright server is available.

---

## Security scanning

FamClaw uses [HoneyBadger](https://github.com/famclaw/honeybadger) to scan skills at two points:

**Install time.** `famclaw skill install <path>` scans with HoneyBadger before writing anything to disk. FAIL verdicts block the install by default.

**Runtime, asynchronously.** Tools used during a conversation are scanned in the background after the turn completes. If a scan fails, the tool is quarantined and filtered out of the next turn. This never adds latency — scanning runs in parallel with or after the response.

All behavior is configurable in `config.yaml` under the `seccheck:` section.

---

## Status

**v0.5.0 — first deployed family release.** v0.5.1 in flight (closes the bugs surfaced by the first real-world install). Phase 3.3 family state shipped (PR #149).

### What works

| Feature | Status |
|---------|--------|
| **Policy gate** | OPA rules for input, tool calls, and output (33 Rego tests) |
| **Pipeline engine** | Composable stages: classify → policy → LLM → tools → output filter |
| **Multi-backend LLM** | OpenAI-compatible: Ollama, llama.cpp, Groq, OpenAI, OpenRouter |
| **Smart tool selection** | Token-budget-aware filtering, role+skill scoping |
| **Context compression** | Tiered truncation keeping system prompt + pinned messages |
| **Agent dispatch** | `spawn_agent` builtin tool — parent LLM delegates to a different profile (default-deny MCP tools, per-call timeout, scheduled with concurrency cap) |
| **Web fetch** | `web_fetch` builtin tool (off by default) — fetch a URL and return extracted text, role-gated + OPA `tool_policy` + per-host allowlist + size/timeout caps. Optional headless-browser fallback for JS-heavy sites (`fallback_to_browser`, off by default; requires `tools.browser.enabled`) |
| **Skill adapters** | FamClaw (SKILL.md), OpenClaw (SOUL.md), Claude Code (.md) |
| **Skill install** | From parent dashboard Skills tab or CLI; HoneyBadger-scanned at install time |
| **llama.cpp sidecar** | Spawns llama-server, GGUF model catalog, TurboQuant support |
| **Security scanning** | Honeybadger runtime stage, install-time + stale scan gates |
| **Web UI** | Chat, parent dashboard, 5-step wizard with AI profiles, PIN-gated skill install/remove |
| **Web auth** | Cookie-based web sessions + machine-bound credential vault (`internal/credstore`, `internal/web/middleware`); see [docs/SECURITY.md](docs/SECURITY.md) |
| **Family state** | Shared per-family memory (`internal/familystate`): allergies, dietary restrictions, important dates, pets + custom parent-managed categories. Safety-critical entries auto-injected into every system prompt via `<family_safety>` block; rest read on-demand via `get_family_state` tool. Kid proposals queue parent approval; parents auto-apply (OPA-gated). Web dashboard at `/family-state.html` + JSON API at `/api/family-state/*`. |
| **Telegram + Discord** | Fully wired gateway bots, message chunking past per-platform limits (4096/2000 chars) |
| **Unknown-account backend** | Strangers messaging the bot are recorded against a parent-controlled queue, never auto-promoted to a user (issue #111 backend) |
| **MCP tools** | Multi-transport (stdio/HTTP/SSE), unified tool registry |
| **LLM profiles** | Multiple named endpoints, per-user assignment via wizard |
| **CI/CD** | CodeQL, govulncheck, SBOM, cosign signing, TruffleHog, race detector on gateway+agent, schema-drift gate, Telegram/Discord integration tests |

### Recommended models

These picks come from on-device benchmarks — **real tool-call tests**, not just
speed/size numbers. FamClaw's #1 requirement is reliable structured tool calling,
since the policy engine delegates hard tasks (web search, calendar, etc.) to the LLM
through tools. Every recommended model passes the tool-call test (3/3 real calls).

| Hardware | Ollama tag | Size | Why |
|----------|------------|------|-----|
| Raspberry Pi 5 / ≤8 GB RAM | `qwen3:1.7b` | 1.3 GB | Fits a Pi 5 *and* makes real tool calls (3/3), with good writing + warmth |
| 16 GB machines | `qwen3:4b` | 2.3 GB | Richer prose, still 3/3 tool calls, comfortable on 16 GB |
| Capable box / 64 GB Mac | `gemma4:31b` | ~20 GB | Apache-2.0, 3/3 tool calls, best age-appropriate creative writing |
| Pi 3 / ≤2 GB | (remote) | — | Gateway only — no recommended model fits this RAM |

> **Avoid:** `phi4-mini` fakes tool calls (0/3 in testing) and `gemma4:e4b`/`gemma4:e2b`
> are too large for a Pi 5's 8 GB RAM.

See [docs/BACKENDS.md](docs/BACKENDS.md) for inference engine comparison.

See [AGENTS.md](./AGENTS.md) for the full build plan.

---

## Testing

### Quick

```bash
CGO_ENABLED=0 go test ./... -count=1
opa test internal/policy/policies/family/ internal/policy/policies/data/ -v
```

### Integration

```bash
CGO_ENABLED=0 go test -tags integration ./e2e/... -count=1
```

REST stubs only — no real bots, no network.

### Behavioral (optional — needs local Ollama)

```bash
make behavioral
```

13 probe×persona pairs against the assembled system prompt.

### Schema golden

Regenerate the SQLite schema fixture:

```bash
UPDATE_SCHEMA_GOLDEN=1 go test ./internal/store/
```

### Prompt snapshots

Regenerate the four persona snapshots:

```bash
UPDATE_PROMPT_SNAPSHOTS=1 go test ./internal/prompt/
```

---

## License

[AGPL-3.0](./LICENSE)
