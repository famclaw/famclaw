# FamClaw — Claude Code Project Instructions

## What this project is
FamClaw is a secure family AI assistant written in Go.
- Runs locally on Raspberry Pi 3/4/5 and Mac Mini
- Every message through any interface goes through a policy engine before the LLM sees it
- Age-aware family profiles (under_8, age_8_12, age_13_17, parent)
- Parental approval workflow for restricted topics
- Gateways: Telegram, WhatsApp, Discord + local web UI
- Fully offline after setup — no cloud dependency
- Single cross-compiled binary, CGO_ENABLED=0

## GitHub organisation: two repos

This project lives across two repositories under the `famclaw` GitHub org:

```
github.com/famclaw/famclaw     ← YOU ARE HERE — core binary
github.com/famclaw/skills      ← separate repo — skill registry
```

### Why two repos
Skills in `famclaw/skills` are shared with PicoClaw and OpenClaw users.
Anyone can install a famclaw skill without touching the core binary:
  famclaw:   famclaw skill install famclaw/seccheck
  PicoClaw:  picoclaw skills install famclaw/seccheck
  OpenClaw:  clawhub install famclaw/seccheck

Keeping skills separate means:
- Community can contribute skills without PRing the core binary
- Skills have independent versioning and releases
- No dependency on famclaw runtime to use a skill

### famclaw/famclaw layout (this repo)
```
famclaw/
├── cmd/famclaw/          # main.go — entry point
├── internal/
│   ├── agent/            # conversation loop, LLM streaming, tool calls
│   ├── classifier/       # keyword → topic category (no LLM)
│   ├── config/           # YAML config + env expansion
│   ├── gateway/          # Telegram, WhatsApp, Discord, web
│   │   ├── gateway.go    # common Message/Reply interface
│   │   ├── router.go     # identity resolve → policy → agent → reply
│   │   ├── telegram/
│   │   ├── whatsapp/
│   │   └── discord/
│   ├── identity/         # gateway account → famclaw user mapping
│   ├── llm/              # OpenAI-compatible HTTP client (streaming)
│   ├── mdns/             # LAN discovery (famclaw.local)
│   ├── mcp/              # MCP client — spawn/manage skill tool servers
│   ├── notify/           # email, slack, discord, sms, ntfy notifications
│   ├── policy/           # OPA evaluator — identical decision for all gateways
│   ├── seccheck/         # security scanner for skills/MCP repos
│   ├── skillbridge/      # SKILL.md parser + install from famclaw/skills
│   ├── store/            # SQLite (modernc — pure Go, no CGO)
│   └── web/              # HTTP + WebSocket server + embedded UI
├── policies/
│   ├── family/           # OPA Rego policies (decision.rego + tests)
│   └── data/             # topics.json taxonomy
├── scripts/
│   ├── install-rpi.sh
│   ├── install-termux.sh
│   ├── build-image.sh    # flashable SD image builder
│   └── firstboot.sh
├── docs/
│   ├── FLASH.md
│   ├── GATEWAYS.md
│   ├── PERSONAS.md
│   ├── SKILLS.md
│   └── HARDWARE.md
├── .github/workflows/
│   ├── ci.yml            # test + build on every PR
│   └── release.yml       # cross-compile + SD images on tag
├── CLAUDE.md             # this file
└── Makefile
```

### famclaw/skills layout (separate repo — DO NOT create here)
```
skills/
├── seccheck/
│   ├── SKILL.md              # AgentSkills spec — works in famclaw, PicoClaw, OpenClaw
│   └── bin/                  # prebuilt binaries attached to GitHub releases
│       ├── seccheck-linux-arm64
│       ├── seccheck-linux-armv7
│       └── seccheck-linux-amd64
├── family-safe-search/
│   └── SKILL.md
├── CONTRIBUTING.md           # how to submit a skill
├── skills.json               # registry index (name, repo, description, tags)
└── .github/workflows/
    └── validate.yml          # validate SKILL.md frontmatter on every PR
```

## Coding rules — MUST follow
1. **CGO_ENABLED=0 always** — every package must cross-compile cleanly
2. **No CGO imports** — use modernc.org/sqlite not mattn/go-sqlite3
3. **Table-driven tests** — every package has `_test.go` with table-driven cases
4. **No global state** — everything injected via constructor
5. **Context everywhere** — all blocking calls take `context.Context` as first arg
6. **Errors wrapped** — `fmt.Errorf("doing X: %w", err)` not bare returns
7. **Interfaces at boundaries** — gateway, notifier, llm client are interfaces
8. **Policy is the gate** — LLM is NEVER called before policy.Evaluate() returns allow
9. **Logs to stderr** — stdout is reserved for MCP JSON-RPC in skill servers
10. **One binary** — no separate processes except MCP skill servers (spawned on demand)

## Policy engine rules — NEVER violate
- `policy.Evaluate()` is called on EVERY message from EVERY gateway
- A "parent" role always returns allow — but identity must be verified first
- Unknown gateway accounts always get the onboarding response — never reach the LLM
- Hard-blocked categories CANNOT be overridden by approvals

## Module path
`github.com/famclaw/famclaw`

## LLM backend — platform rules

famclaw talks to ANY OpenAI-compatible HTTP endpoint. The `api_key` field is
optional — omit it for Ollama on LAN, set it for cloud providers.

| Platform | Backend | api_key needed |
|---|---|---|
| RPi 3/4/5 | Ollama (local, auto-installed by firstboot.sh) | No |
| Mac Mini | Ollama (local) | No |
| Old Android (Termux) | OpenAI / Anthropic / OpenRouter / another device's Ollama | Yes (or LAN URL) |
| Any device | Can point at RPi's Ollama on LAN | No |

**NEVER install or start Ollama on Android.** The install-termux.sh script
must prompt the user to choose a provider instead.

`internal/llm/client.go` must send `Authorization: Bearer <api_key>` header
when `cfg.LLM.APIKey` is non-empty. Omit the header entirely when empty
(Ollama ignores it but some proxies reject unexpected headers).

## Key dependencies
```
github.com/gorilla/websocket      v1.5.3   — WebSocket for web UI
github.com/grandcat/zeroconf      v1.0.0   — mDNS (famclaw.local)
github.com/open-policy-agent/opa  v0.68.0  — policy engine
modernc.org/sqlite                v1.33.1  — pure Go SQLite
gopkg.in/yaml.v3                  v3.0.1   — config parsing
github.com/go-telegram-bot-api/telegram-bot-api/v5  v5.5.1
github.com/bwmarrin/discordgo     v0.28.1
go.mau.fi/whatsmeow               latest   — WhatsApp (pure Go)
```

## Test requirements
- Every package: `go test ./internal/PACKAGE/...`
- Policy: `opa test ./policies/`
- Integration: `go test ./... -tags integration`
- Coverage target: >80% on policy, identity, classifier, gateway/router
- CI blocks merge if tests fail or binary doesn't cross-compile to arm64

## Build targets (all CGO_ENABLED=0)
```
make build          # current platform
make cross-rpi3     # linux/arm/v7
make cross-rpi4     # linux/arm64  (also rpi5)
make cross-android  # android/arm64 + android/arm
make cross-mac-arm  # darwin/arm64
make cross-linux64  # linux/amd64
make cross          # all of the above
```

## What exists already
See individual files. Do not regenerate files that already exist unless fixing a bug.
Existing files: cmd/famclaw/main.go, internal/agent/agent.go, internal/config/config.go,
internal/llm/client.go, internal/mdns/mdns.go, internal/seccheck/scanner.go,
internal/store/db.go, internal/web/server.go, internal/web/static/index.html,
scripts/install-rpi.sh, scripts/install-termux.sh, scripts/famclaw.service,
config.yaml, Makefile, go.mod, README.md

## What needs to be built — in order
See AGENTS.md for the multi-agent build plan.
