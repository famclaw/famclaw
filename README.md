# 🛡️ FamClaw

**A secure, local-first family AI gateway. Runs on Raspberry Pi, Mac, or any Linux box.**

FamClaw is a lightweight Go gateway that connects your family to any AI model — local or cloud — through Telegram, Discord, and a web interface. Every message goes through a policy engine before the AI ever sees it.

---

## What it is

- **A gateway, not an AI.** FamClaw routes messages between your family and whatever LLM you configure — Ollama on your home server, OpenAI, Anthropic, OpenRouter, or any OpenAI-compatible endpoint.
- **A policy enforcer.** Every message is evaluated by OPA (Open Policy Agent) before reaching the LLM. Kids get age-appropriate responses. Sensitive topics require parental approval.
- **A family assistant.** Age-aware profiles, parental approval workflow, notification to parents via email/SMS/Slack/Discord/ntfy.

---

## How it works

```
Family member sends message
  → via Telegram / Discord / Web UI
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
  base_url: "http://<your-host>:11434"  # Ollama on your LAN device
  model: "llama3.2:3b"
  # Per-call LLM request timeout in seconds. Each chat/tool call gets its
  # own context deadline. Default: 300 (5 minutes).
  timeout_seconds: 300

  profiles:
    cloud:
      label: "OpenAI cloud (gpt-4o-mini)"  # display-only name shown in the web UI
      base_url: "https://api.openai.com/v1"
      model: "gpt-4o-mini"
      api_key: "${OPENAI_API_KEY}"

  # When a message carries an image attachment (a photo sent on Telegram
  # or Discord), FamClaw routes it to this LLM profile instead of the
  # normal per-user model — text-only messages always use the normal
  # endpoint. Set this to a vision-capable model (e.g. qwen2.5-vl,
  # llama3.2-vision, gemma3).
  #
  # When EMPTY, the per-user endpoint is used for images too. If that
  # model is text-only, the image is still sent to it but CANNOT be seen
  # — it is silently ignored (the assistant only receives the empty text),
  # which is why real deployments set vision_profile. Images are sent to
  # the configured LLM endpoint, which may be remote — they stay on-device
  # only when that endpoint is local.
  vision_profile: ""
```

**Profile labels:** the optional `label` on each profile is a display-only name
shown in the web UI. Keep it accurate to where the profile actually routes
(host + model), so a family member reading the label knows which model answers —
a stale label (e.g. naming a model the profile no longer serves) misdescribes
the routing.

**Security note:** `llm.api_key` is loaded from plaintext YAML by default.
Set `FAMCLAW_LLM_API_KEY` environment variable to override — it takes precedence and avoids logging the plaintext warning.

**Reasoning auto-detection.** When the endpoint is a LiteLLM (or OpenAI-compatible) gateway, FamClaw queries its model metadata at startup and learns each model's `litellm_params.merge_reasoning_content_in_choices` setting, so a final answer that ships in the reasoning field is shown to the family while chain-of-thought is filtered out — no per-model hand-configuration. Startup logs `Auto-detect: N models discovered`, or a warning when the gateway is unreachable (the built-in heuristic then applies, and later requests retry detection). Opt out or pin models with `llm.reasoning` (`enable_auto_detect: false`, `litellm_url`, `per_model_override`).

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
Download the binary for your platform from the [latest release](https://github.com/famclaw/famclaw/releases/latest):

| Platform | Artifact |
|---|---|
| Apple Silicon Mac | `famclaw-darwin-arm64.tar.xz` |
| Intel Mac | `famclaw-darwin-amd64.tar.xz` |
| Linux x86_64 | `famclaw-linux-amd64.tar.xz` |
| Linux arm64 | `famclaw-linux-arm64.tar.xz` |
| Linux arm (32-bit, e.g. RPi 3/Zero) | `famclaw-linux-armv7.tar.xz` |

```bash
tar -xJf famclaw-darwin-arm64.tar.xz
touch config.yaml   # start empty — the web wizard fills it in
./famclaw --config config.yaml
```

Then open `http://<host>:8080` and complete the setup wizard (it writes the
rest of the config for you).

Release darwin binaries are Developer ID-signed and notarized when signing
credentials are configured; without them the release falls back to
ad-hoc-signed darwin binaries (a browser download needs Gatekeeper's
right-click → Open on first run — `docs/RELEASE.md`). The tarballs verify
against `checksums.txt` plus a cosign signature bundle.
Upgrading an existing installation (atomic swap + service restart, never an
in-place overwrite):

```bash
curl -fsSL https://raw.githubusercontent.com/famclaw/famclaw/main/scripts/update.sh | bash
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

Each family member's gateway account maps to their profile. Emma's Telegram account → Emma's age policy. Parent's Discord account → parent access.

**File delivery.** When the assistant produces a file in the conversation workspace (e.g. via `file_write`), it can attach and send it back into the same conversation through the `send_file` built-in tool — currently on Discord (the group channel or DM the user is in). Files are confined to the conversation sandbox, capped at 25 MiB (Discord's bot upload limit), and every delivery is audit-logged. Which roles may call the tool is decided by OPA `tool_policy`; on gateways without file support yet, the tool returns an honest "not available" error instead of silently dropping the request.

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

Skills are installed from the parent dashboard (Skills tab) via `/api/skills/install`, or manually by placing the `SKILL.md` in the skills directory. The `famclaw skill` CLI is not yet implemented.

MCP tool servers (stdio, HTTP, or SSE) can be added and removed from the parent dashboard (MCP Servers panel) via `/api/mcp/add` and `/api/mcp/remove`, or manually in `config.yaml` under `skills.mcp_servers`. The panel lets you specify a name, transport, and transport-specific fields (command and args for stdio, URL for HTTP/SSE), with client-side validation before the server is saved. Configured servers are registered with the MCP tool pool on the next startup. The running pool is reloaded in place on config change (see *Configuration hot-reload* in `docs/AGENT_SETUP.md`).

Parents can also manage MCP servers **conversationally** from within a chat, using two parent-only built-in tools:

- **`mcp_list`** — lists every configured MCP server with its transport (stdio/HTTP/SSE) and live load status (loaded, error, or not-yet-loaded).
- **`mcp_add`** — registers a new MCP server: validates the configuration, saves it to `config.yaml` under `skills.mcp_servers`, reloads the running MCP pool, and reports the resulting status. If the new server cannot be loaded by the running pool (e.g. the command does not exist), `mcp_add` reports the error and reassures you that the server is saved and will load on restart.

Both tools are **parent-only** — a child cannot add or inspect MCP servers. These complement the dashboard panel; use whichever is more convenient.

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

Off by default. When enabled, the LLM gets a `web_fetch` tool that retrieves a URL and returns extracted text — `text/html` is parsed via `golang.org/x/net/html` and stripped of `<script>`/`<style>`/`<head>`; `text/plain` and `application/json` pass through; `application/pdf` has its text layer extracted with a pure-Go parser (so `CGO_ENABLED=0` is preserved). A scanned/image-only PDF has no text layer — the tool returns a neutral note instead of failing, so the assistant knows it's a coverage limit, not a broken fetch; a PDF truncated to `tools.web_fetch.max_bytes` is an explicit, actionable error. Useful for "what's the weather", "look up the docs page for X", and similar fetches.

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
    block_private_networks: false  # opt-in: block loopback/RFC1918/ULA at the dialer
    fallback_to_browser: false  # opt-in: fall back to headless browser for JS-heavy sites. REQUIRES both tools.browser.enabled=true AND a reachable Playwright browser pool — enabling the flag alone is NOT sufficient; if the browser pool is nil or unreachable, the fallback never fires and web_fetch silently returns the plain fetch text (a warning is logged).
    fallback_min_text_length: 10 # below this many chars of extracted text, attempt the browser fallback
```

Defense in depth:

- **Role gate** at registration — the tool is only added to the LLM's tool list for users in `allowed_roles`.
- **OPA `tool_policy` rule** at the tool loop — `parent` and `age_13_17` are allowed; `under_8` and `age_8_12` are denied. Blocked calls never dispatch.
- **URL allowlist** in `handleWebFetch` — only `http`/`https` schemes; the request host must equal an allowlist entry or be a subdomain of one. **An empty allowlist denies all fetches** (SSRF guard) — operators must list the hosts they trust. The same predicate is re-applied to every redirect target inside `webfetch.Fetch`.
- **Private-network access** — By default FamClaw *allows* private-network access (it is a home-LAN product that must reach co-located SearXNG, Playwright, llama-server). The host allowlist still applies regardless. To block loopback/RFC1918/RFC4193 ULA at the dialer, set `tools.web_fetch.block_private_networks: true`.
- **Size + timeout caps** in `internal/webfetch` — `MaxBytes` enforced via `io.LimitReader`, redirect chain capped at 5 hops, request `Timeout` from config.

The fetcher itself is in `internal/webfetch/`; the agent handler lives in `internal/agent/agent.go` (`handleWebFetch`).

For JS-heavy sites where HTML→text extraction yields too little content, `web_fetch` can fall back to a headless browser (the built-in `browser` tool), reusing the same host allowlist. This fallback is **off by default** (`tools.web_fetch.fallback_to_browser: false`).

**The browser pool must be configured and reachable.** Enabling `fallback_to_browser` is not sufficient on its own — you must also set `tools.browser.enabled: true` with a working `tools.browser.endpoint` (a Playwright browser server, e.g. `ws://localhost:3000/`). If the browser pool is nil or unreachable (no endpoint configured, or Playwright server down), the fallback **never fires**: `web_fetch` returns the plain fetch text and emits a visible log line so you can see the degradation rather than silently getting thin content. Check the logs if fetch results look unexpectedly short after enabling the fallback.

When the fallback is active and the plain fetch returns fewer than `fallback_min_text_length` chars, the browser navigates and extracts rendered text. Every failure path returns a distinct, honest error — a nil/unavailable browser pool, a browser fetch failure, or an empty rendered result — so an empty page is never silently returned as a successful fetch. Integration tests for the live browser path live behind the `integration` build tag (`go test -tags integration ./internal/agent/ -run TestFetchWithBrowser_Integration`), skipped cleanly where no Playwright server is available. The merge-queue CI lane runs the live browser test against a containerized Playwright server.

**Bot-blocked fetches trigger the same fallback.** A direct fetch to an allowlisted host that fails with HTTP 403 or 429 (an upstream engine or CDN bot-blocking the plain HTTP client — the page itself is usually fine in a real browser) retries through the headless browser, when `fallback_to_browser` is on and the browser pool is reachable. No new gate is added: the host allowlist is re-checked against the final URL after redirects, so a 403/429 on an allowlisted host can never widen the fetch surface. 404, 5xx, and transport-level failures are not bot-blocking, so the fallback deliberately does not fire for them — the original error is surfaced as-is (both attempts are logged). If the browser attempt also fails, the original HTTP error is returned.

## Web search (`web_search`)

Off by default. When enabled, the LLM gets a `web_search` tool that queries a search **provider** and returns structured title/url/snippet results. Use it before `web_fetch` when you don't already know an exact URL — results carry concrete URLs and snippets you can answer from directly.

Enable in `config.yaml`:

```yaml
tools:
  web_fetch:
    enabled: true              # required — web_search reuses web_fetch's host allowlist
    url_allowlist:
      - localhost              # your SearXNG host (SearXNG provider only)
      - wikipedia.org
  web_search:
    enabled: true
    allowed_roles: [parent]    # role gate — checked when registering the tool
    provider: searxng          # searxng (default) | tavily — see "Providers" below
    endpoint: "http://localhost:8888"   # SearXNG JSON search URL (SearXNG only)
    # tavily_api_key: "..."    # optional; the FAMCLAW_WEB_SEARCH_API_KEY env var wins
    max_results: 8             # default 8, hard-capped at 16
    timeout_seconds: 30        # default 30 — raises the old 10s default for self-hosted SearXNG fanning out to multiple upstreams
```

**Providers — the operator options.** The web-retrieval plan defines five operator options (A–E); A and B are implemented now, C/D are the same shape and arrive later, and E is not a search provider at all:

- **Option A — SearXNG (default, private, $0).** A self-hosted JSON relay; keep it as the default. One honest caveat: SearXNG fans out to upstream engines, and engines that bot-block relay traffic (429/CAPTCHA) shrink its pool — a live audit of a typical home instance in October 2026 found 5 of 7 engines blocked (brave 429, DuckDuckGo CAPTCHA, Mojeek/Mwmbl/Qwant access-denied), with only Bing, Wikipedia, and Wiby surviving. A correctly configured SearXNG is therefore a **best-effort private floor** (100% on-LAN, zero vendors), not a reliability guarantee — results can be sparse depending on your instance.
- **Option B — Tavily (hosted, `provider: tavily`).** A hosted search API — one signup, no card, 1,000 free credits/month, and the same key later covers hosted extraction (option E). No `endpoint` and no allowlist entry: the request goes to a fixed vendor endpoint constant (`api.tavily.com/search`), which is deliberately *not* an `url_allowlist` host — that gate covers user-supplied `web_fetch` targets, and adding the vendor host there would conflate the two. **Data-flow disclosure:** enabling a hosted provider sends the user's query text to that provider's API. The key comes from the `FAMCLAW_WEB_SEARCH_API_KEY` environment variable (preferred) or `tools.web_search.tavily_api_key` in YAML (env wins; a YAML-set key is logged as a warning, not an error).
- **Options C/D — Exa / Brave.** The same provider interface is designed for these second hosted indexes with the identical config shape (`provider` + API key); the adapters have not landed yet. An unimplemented (or unknown) `provider` value is a config error listing what is supported, so a typo never silently falls back to a different backend.
- **Option E — hosted extraction (e.g. Firecrawl)** is a later *extraction* strengthening, not a `web_search` provider: it would send fetched page content to a hosted API only when the local fetch and the browser fallback both fail. It is not part of the search config today.

**Host gate (SearXNG provider):** `web_search` is not independent — the default SearXNG provider requires `tools.web_fetch.enabled=true` and reuses the `tools.web_fetch.url_allowlist` as its host gate. The search endpoint host (e.g. `localhost`) must be allowlisted; an empty allowlist denies all searches. A hosted provider (`tavily`) has no operator-supplied endpoint, so neither `tools.web_fetch.enabled` nor a `url_allowlist` entry applies — the host gate covers user-supplied `web_fetch` targets, not the vendor's own API.

**Dependency:** a running SearXNG instance (or any OpenSearch-compatible JSON search endpoint) for the default provider — install SearXNG via Docker or run it locally and point `endpoint` at its `search` JSON URL. For `provider: tavily`, no endpoint at all — just the `FAMCLAW_WEB_SEARCH_API_KEY` env var.

**Failure behavior — class-specific, honest, and visible.** `web_search` never returns an empty result set when the backend is down, and it never invents results: the LLM is instructed to relay the failure honestly ("I could not search right now"). The details:

- **Bounded retry.** Transient failures (HTTP 429, 5xx, timeout) are retried — up to 3 attempts total — honoring the provider's `Retry-After` when present and bounded by the request timeout. Non-transient classes (401/403, malformed payload, connection refused) are not retried.
- **Class-specific markers.** Each failure class maps to a short neutral marker the LLM can relay in the user's language: "rate limited", "search backend error" (5xx), "search timed out", "search backend unreachable" (connection), "search backend auth or config error" (401/403), "invalid search backend response" (malformed), with a generic "unavailable" fallback. Config problems surface distinctly too: an unknown/unsupported `provider` value or a hosted provider without a key is a "provider not configured" error, and a non-allowlisted endpoint host is a "host not permitted" error. No internal endpoint detail is ever echoed into chat (the logs carry the sanitized endpoint; a Tavily key is never logged).
- **Zero hits are normal.** A 200 with no hits is a legitimate "no results" outcome, distinct from an unavailable backend.
- **A real startup probe, not just a reachability ping.** At boot, FamClaw issues an actual `format=json` search request (a recognizable marker query, greppable in your SearXNG logs) and verifies the response is a usable JSON payload with a `results` field — not merely any HTTP status. Failures log a `WARNING` with an actionable, class-specific message: a 200 that is not JSON means the endpoint answers but JSON is disabled ("is `format=json` enabled on SearXNG?"), 401/403 is auth/config, 429 means upstream engine bot-blocking (see the SearXNG caveat under Options), 5xx is a provider server error, and a timeout says "slow, not necessarily down" — distinct from a connection failure. The probe is best-effort and never blocks startup; a hosted provider has no local endpoint to probe, so it is skipped with a note.

---

## Security scanning

FamClaw uses [HoneyBadger](https://github.com/famclaw/honeybadger) to scan skills at two points:

**Install time.** Installing a skill from the dashboard (`/api/skills/install`) scans it with HoneyBadger before writing anything to disk. FAIL verdicts block the install by default.

**Runtime, asynchronously.** Tools used during a conversation are scanned in the background after the turn completes. If a scan fails, the tool is quarantined and filtered out of the next turn. This never adds latency — scanning runs in parallel with or after the response.

The master switch is `seccheck.enabled: true` in `config.yaml` (it is **not** auto-enabled from `auto_seccheck`/`runtime_scan` alone — a gate that reads as "armed" while the scanner binary is absent is the exact false promise FamClaw avoids).

### Scanner availability (famclaw owns its scanner dependency)

HoneyBadger is a dependency of famclaw, not a manual step. FamClaw fetches and installs it for you:

- `make install` pulls the pinned `honeybadger` binary via `go install` before copying the famclaw binary (so a fresh machine ends up with a working gate).
- At startup, if `seccheck.enabled` is true and the binary is missing, famclaw runs `EnsureScanner` (`go install github.com/famclaw/honeybadger/cmd/honeybadger@<seccheck.scanner_version>`) so `go install` users get it automatically too.

**When the scanner genuinely cannot be made available** (no `go` toolchain, no network, pull failure), FamClaw fails **closed**, never waving a skill through unscanned:

- A skill install is **refused** with a clear error (no file is written).
- Boot-load scanning of pre-installed skills logs a per-skill visible warning and the state is surfaced.
- A `seccheck_reports` row is never written for an unscanned skill, so operators can audit "did this actually scan?"

All of this is verified live: installing famclaw and enabling `seccheck.enabled: true` ends with a `seccheck_reports` row for every scanned skill.

All behavior is configurable in `config.yaml` under the `seccheck:` section.

---

## Behavior changes in v0.9.0

### Photo → Action (#305) — the headline feature
FamClaw can now ACT on an image, not just describe it: a photo of an item can drive "add to inventory", a photo of an event can drive a calendar entry.

This feature works by splitting the request into two steps due to a limitation in some models:
- When a message contains both an image attachment and tool calls, certain models (like gemma-4-26b) silently fail to emit tool calls. 
- Instead of one combined image+tools request, FamClaw now runs a two-step process:
  1. First, send the image with NO tools and a factual-description prompt to get a description (with a fixed token budget of 1000 tokens to ensure the reasoning model has room to generate content)
  2. Then, feed that description back as text into the tool-enabled call, stripping the image so the model can act on what it saw.

This makes photo-to-action work on the captain's own hardware without any model or deployment changes.

### famclaw now works with llama.cpp-backed local models (#304)
Previously, FamClaw omitted the `content` key on empty non-assistant messages. While vLLM tolerated this, **llama.cpp rejects it with a 400**, making FamClaw unusable with any llama.cpp-served model and silently failing every single message. 

This change makes it possible for a family assistant to run on local hardware rather than a vLLM box. The fix ensures that non-assistant roles (system, user, tool) always emit a "content" key, even when empty, which llama.cpp requires.

### Children can use file tools, with approval on executables (#301)
Children can now use `file_read`, `file_stat`, `file_list`, and non-executable `file_write` directly. However, a write whose CONTENT or TARGET looks executable (shebang, .sh/.bash extension, ELF/Mach-O magic bytes) routes to parental approval instead of being refused outright.

The child sees the file tools available in their tool list, but when they try to write an executable file, they'll get a notification to their parent for approval. `confinePath` - Go-level path validation - is the containment mechanism; it does NOT rely on kernel sandboxing (Landlock only wraps MCP subprocesses).

### Enabling image support — a deployment step people will miss
Serving a multimodal model is not enough. Two things are required and both were missed on the captain's own Mac:
1. The model server must be started with its vision projector (for llama.cpp: `--mmproj <projector.gguf>`), or images fail with "image input is not supported ... you may need to provide the mmproj";
2. FamClaw's config needs a `vision_profile` pointing at a profile that reaches that model, or images are never routed to it.

### Voice message transcription
Voice notes sent on Telegram and Discord are now transcribed into text and handled exactly like a typed message — they pass through the same age/approval policy before the LLM sees them, so a spoken request gets the same gating as a typed one. Voice transcription is **off by default** and requires a local speech-to-text service (e.g. whisper.cpp started with `--convert`, or a LiteLLM gateway in front of one).

Enable in `config.yaml`:

```yaml
tools:
  transcription:
    enabled: true
    endpoint: "http://<your-host>:8092"   # base URL of your /v1/audio/transcriptions service
    model: "whisper-1"                       # model name sent to the service
    max_bytes: 26214400                      # cap on downloaded audio; default 25 MB
    timeout_seconds: 30                      # per-request timeout; default 30
```

When `enabled` is not set (or `false`), a voice message is **not** silently dropped — the assistant replies with a visible "voice isn't available" notice so the sender knows to either configure transcription or rephrase. Audio is sent as a multipart upload to `<endpoint>/v1/audio/transcriptions`; the transcript becomes the user's message. See [docs/GATEWAYS.md](./docs/GATEWAYS.md) for per-gateway details.

### Additional notes
`tools.web_fetch.url_allowlist` matches on a dot boundary, so an entry like `gov` permits `clinicaltrials.gov` and `pubmed.ncbi.nlm.nih.gov` but NOT `evilgov.com`. This is genuinely useful and non-obvious.

---

## Status

**v0.11.2** — current release. **v0.12.0** — upcoming.

### What works

| Feature | Status |
|---------|--------|
| **Policy gate** | OPA rules for input, tool calls, and output (90 Rego tests) |
| **Pipeline engine** | Composable stages: classify → policy → LLM → tools → output filter |
| **Multi-backend LLM** | OpenAI-compatible: Ollama, llama.cpp, Groq, OpenAI, OpenRouter |
| **Smart tool selection** | Token-budget-aware filtering, role+skill scoping |
| **Context compression** | Tiered truncation keeping system prompt + pinned messages |
| **Tool-result spillover cache** | Large tool results (>head budget, ~8.5 KB at 128k ctx — 2% of the context window, floored at 512 B and capped at 64 KB) spill to a per-user file cache with TTL + LRU eviction; smaller results stay inline. `builtin__tool_result_more` reads the tail | **Agent dispatch** | `spawn_agent` builtin tool — parent LLM delegates to a different profile (default-deny MCP tools, per-call timeout, scheduled with concurrency cap) |
| **Web fetch** | `web_fetch` builtin tool (off by default) — fetch a URL and return extracted text (HTML→text, plain/JSON passthrough, PDF text layer), role-gated + OPA `tool_policy` + per-host allowlist + size/timeout caps. Optional headless-browser fallback for JS-heavy sites **and on 403/429 bot-block** (`fallback_to_browser`, off by default; requires `tools.browser.enabled` AND a reachable browser pool; the host allowlist is re-checked on the final URL) |
| **Web search** | `web_search` builtin tool (off by default) — query a search provider: SearXNG (default; requires `tools.web_fetch.enabled=true` and reuses its `url_allowlist` as the host gate) or hosted Tavily (`provider: tavily` + `FAMCLAW_WEB_SEARCH_API_KEY` env var; query text goes to the vendor); 30s timeout; bounded retry (3 attempts, Retry-After aware); class-specific honest error markers + a real `format=json` startup probe |
| **Voice transcription** | `transcription` builtin (off by default) — transcribes Telegram/Discord voice notes via a local `/v1/audio/transcriptions` service; default 25 MB cap, 30s timeout. See [docs/GATEWAYS.md](./docs/GATEWAYS.md) |
| **Skill adapters** | FamClaw (SKILL.md), OpenClaw (SOUL.md), Claude Code (.md) |
| **Skill install** | From parent dashboard Skills tab; HoneyBadger-scanned at install time |
| **llama.cpp sidecar** | Spawns llama-server, GGUF model catalog, TurboQuant support |
| **Security scanning** | Honeybadger runtime stage, install-time + stale scan gates |
| **Web UI** | Chat, parent dashboard, 5-step wizard with AI profiles, PIN-gated skill install/remove |
| **Web auth** | Cookie-based web sessions + machine-bound credential vault (`internal/credstore`, `internal/web/middleware`); parent PIN login plus per-child join codes (parent-issued, revocable, `vault_secrets`-stored, no plaintext retention) so children get a session without the parent PIN; WebSocket origin policy `same-origin`/`strict`; wildcard binds require a 32+ char `server.secret`. See [docs/SECURITY_MODEL.md](docs/SECURITY_MODEL.md) |
| **Family state** | Shared per-family memory (`internal/familystate`): allergies, dietary restrictions, important dates, pets + custom parent-managed categories. Safety-critical entries auto-injected into every system prompt via `<family_safety>` block; rest read on-demand via `get_family_state` tool. Kid proposals queue parent approval; parents auto-apply (OPA-gated). Web dashboard at `/family-state.html` + JSON API at `/api/family-state/*`. |
| **Reminders** | Per-user scheduled reminders (`internal/reminder`). A delivery failure is never silent: a dead target (404 — deleted channel, removed bot) fast-paths with the target marked invalid for the process (no 404 storm), the reminder's content is recorded in the `undelivered_reminders` table, and the user is notified on an alternate reachable gateway when one exists (e.g. dead Discord channel → Telegram). Transient failures keep bounded retries (3 attempts). Parents can query what was missed at `/api/reminders/undelivered`. |
| **Telegram + Discord** | Fully wired gateway bots, message chunking past per-platform limits (4096/2000 chars) |
| **Unknown-account backend** | Strangers messaging the bot are recorded against a parent-controlled queue, never auto-promoted to a user (issue #111 backend) |
| **MCP tools** | Multi-transport (stdio/HTTP/SSE), unified tool registry |
| **LLM profiles** | Multiple named endpoints, per-user assignment via wizard |
| **CI/CD** | CodeQL, govulncheck, SBOM, cosign signature bundles, Developer ID signing + notarization of darwin release binaries on macos-latest (ad-hoc-signed fallback when signing credentials are missing — `docs/RELEASE.md`), TruffleHog, race detector on gateway+agent, schema-drift gate, Telegram/Discord integration tests |

### Recommended models

These picks come from on-device benchmarks — **real tool-call tests**, not just
speed/size numbers. FamClaw's #1 requirement is reliable structured tool calling,
since the policy engine delegates hard tasks (web search, calendar, etc.) to the LLM
through tools. Every recommended model passes the tool-call test (3/3 real calls).

| Hardware | Ollama tag | Size | Why |
|----------|------------|------|-----|
| Raspberry Pi 5 / ≤8 GB RAM | `qwen3:1.7b` | 1.3 GB | Fits a Pi 5 *and* makes real tool calls (3/3), with good writing + warmth |
| 16 GB machines | `qwen3:4b` | 2.3 GB | Richer prose, still 3/3 tool calls, comfortable on 16 GB |
| Capable box / 64 GB Mac | `gemma4:31b` | ~20 GB | Gemma licence, 3/3 tool calls, best age-appropriate creative writing |
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

### Classification eval corpus

The keyword classifier is scored against a hand-authored multilingual corpus at `internal/classifier/testdata/classification_corpus.json` (a full 420-sample labeled corpus across `en/es/fr/de/zh`: ~45% benign, ~30% in-scope risky, ~25% adversarial variants such as paraphrase, negation, leetspell/case, keyword-buried, and near-miss phrasings). `internal/classifier/eval_test.go` recomputes per-category P/R/F1, per-language critical-category recall, and benign FPR, then checks them against the committed `internal/classifier/testdata/classification_results.json`. The critical-category set is exactly `{self_harm, hate_speech, illegal_activity, sexual_content}`. Thresholds (critical recall ≥ 95%, benign FPR ≤ 5%) are **recorded targets, not build gates** — the results artifact documents the current English-only keyword gap across `en/es/fr/de/zh`. Regenerate the artifact after changing the corpus:

```bash
FAMCLAW_REGEN_CLASSIFIER_EVAL=1 go test -run TestEvalRegenerate ./internal/classifier/
```

---

## License

[AGPL-3.0](./LICENSE)
