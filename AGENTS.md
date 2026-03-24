# FamClaw — Multi-Agent Build Plan

This file coordinates parallel Claude Code sub-agents.
Each agent has a bounded scope, clear inputs/outputs, and defined test gates.

---

## Two-repo structure

This is `famclaw/famclaw` — the core binary.
Skills live in a **separate repo**: `famclaw/skills`.

Agent 8 (seccheck skill) produces files for the `famclaw/skills` repo.
Do NOT create a `skills/` directory inside this repo.
The `internal/skillbridge` package installs skills FROM `famclaw/skills`,
it does not host them here.

---

```
[1-classifier] ──┐
[2-policy]     ──┼──► [4-gateway] ──► [7-integration-tests]
[3-notify]     ──┤         ▲
[3-identity]   ──┘         │
                      [5-skillbridge]
                      [6-mcp]
                           │
                      [8-seccheck-skill]
                           │
                      [9-sd-image]
                      [10-docs]
```

Agents 1, 2, 3a (notify), 3b (identity) can run in parallel.
Agent 4 (gateway) starts only after 1+2+3a+3b all pass tests.
Agents 5, 6 can run in parallel after 4.
Agent 9 (SD image) runs after all code agents pass.

---

## Agent 1 — classifier

**Scope:** `internal/classifier/`

**Deliverables:**
- `classifier.go` — `New() *Classifier`, `Classify(text string) Category`
- `classifier_test.go` — table-driven, min 30 test cases covering all categories
- `categories.go` — all Category constants as typed strings

**Rules:**
- No external dependencies — pure Go, stdlib only
- No LLM calls — keyword matching only, must be fast (<1ms per call)
- Must cover every category in `policies/data/topics.json`
- Test must include edge cases: empty string, gibberish, mixed language hints

**Done when:** `go test ./internal/classifier/... -v` passes, 0 failures

---

## Agent 2 — policy

**Scope:** `internal/policy/`, `policies/`

**Deliverables:**
- `internal/policy/evaluator.go` — `NewEvaluator(policyDir, dataDir string)`, `Evaluate(ctx, Input) Decision`
- `internal/policy/types.go` — `Input`, `UserInput`, `QueryInput`, `Decision` structs
- `internal/policy/evaluator_test.go` — table-driven Go tests wrapping OPA
- `policies/family/decision.rego` — main policy
- `policies/family/decision_test.rego` — OPA unit tests (min 15 cases)
- `policies/data/topics.json` — topic taxonomy

**Rules:**
- OPA embedded — no external OPA process
- Policy must be evaluated identically regardless of which gateway sent the message
- Hard-blocked categories CANNOT be unlocked by approvals — test this explicitly
- Parent role always returns allow — test this
- Unknown age_group defaults to most restrictive (under_8 rules)

**Test gate 1:** `opa test ./policies/ -v` — all pass
**Test gate 2:** `go test ./internal/policy/... -v` — all pass

**Done when:** both test gates pass

---

## Agent 3a — notify

**Scope:** `internal/notify/`

**Deliverables:**
- `notifier.go` — `MultiNotifier`, `NewMultiNotifier(cfg, secret)`, `Notify(ctx, *Approval, approveURL, denyURL)`, `NotifyDecision(ctx, *Approval)`, `GenerateToken(id, action, secret) string`, `VerifyToken(id, action, token, secret) bool`
- `email.go` — SMTP with HTML approval template
- `slack.go` — Slack webhook
- `discord_notify.go` — Discord webhook (NOT the gateway bot)
- `sms.go` — Twilio REST
- `ntfy.go` — ntfy.sh (self-hosted push)
- `notifier_test.go` — mock all channels, test dispatch logic and token generation

**Rules:**
- Each channel is an interface `Notifier` — easily mockable
- Failed channel never blocks other channels (goroutine per channel)
- `GenerateToken` uses HMAC-SHA256 — test collision resistance
- No channel enabled by default — all require explicit `enabled: true` in config

**Done when:** `go test ./internal/notify/... -v` passes

---

## Agent 3b — identity

**Scope:** `internal/identity/`

**Deliverables:**
- `store.go` — CRUD: `LinkAccount(userName, gateway, externalID)`, `Resolve(gateway, externalID) (*User, error)`, `IsRegistered(gateway, externalID) bool`
- `onboarding.go` — `OnboardingMessage() string`, `UnknownAccountMessage() string`
- `identity_test.go` — table-driven, covers link/resolve/unknown/collision

**Schema additions to `internal/store/db.go`:**
```sql
CREATE TABLE IF NOT EXISTS gateway_accounts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_name   TEXT NOT NULL,
    gateway     TEXT NOT NULL,  -- telegram|whatsapp|discord
    external_id TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(gateway, external_id)
);
```

**Rules:**
- Unknown external_id always returns nil user — NEVER a default
- Gateway name is always lowercase: "telegram", "whatsapp", "discord"
- One external_id can only map to one user — enforce at DB level (UNIQUE constraint)
- Thread-safe — multiple gateways call Resolve concurrently

**Done when:** `go test ./internal/identity/... -v` passes

---

## Agent 4 — gateway

**Scope:** `internal/gateway/`

**Depends on:** agents 1, 2, 3a, 3b all done

**Deliverables:**
- `gateway.go` — `Message` struct, `Gateway` interface, `Reply` struct
- `router.go` — `Router`: receives Message → identity.Resolve → policy.Evaluate → agent.Chat → Reply
- `telegram/bot.go` — long-poll Telegram Bot API
- `whatsapp/bot.go` — WhatsApp via whatsmeow
- `discord/bot.go` — Discord via discordgo
- `router_test.go` — mock all deps, table-driven tests for every policy outcome
- `telegram/bot_test.go` — mock Telegram API
- `discord/bot_test.go` — mock Discord API

**Message flow (router MUST implement exactly this):**
```
1. Receive Message{gateway, external_id, text}
2. identity.Resolve(gateway, external_id)
   → nil: send OnboardingMessage, stop
3. classifier.Classify(text)
4. policy.Evaluate(user, category, approvals)
   → block:            send policy block message, log, stop
   → request_approval: store approval, notify parent, send "waiting" message, stop
   → pending:          send "still waiting" message, stop
   → allow:            continue to step 5
5. agent.Chat(ctx, user, text, streamCallback)
6. send reply
```

**Rules:**
- LLM is NEVER called unless step 4 returns allow — test this with a mock that panics if called
- Streaming: web UI gets tokens via WebSocket, messaging gateways get full response (no streaming)
- Gateway bots run as goroutines — crash in one gateway never affects others
- All gateway tokens come from config — never hardcoded

**Done when:**
- `go test ./internal/gateway/... -v` passes
- Mock-LLM-panic test confirms policy gate works

---

## Agent 5 — skillbridge

**Scope:** `internal/skillbridge/`

**Deliverables:**
- `skill.go` — `Skill` struct, `ParseSKILLMD(path) (*Skill, error)`
- `registry.go` — `Install(repoURL)`, `List()`, `Remove(name)`, `Enable/Disable(name)`
- `loader.go` — `LoadForPrompt(skills []*Skill) string` — injects skills into system prompt (AgentSkills XML format, OpenClaw/PicoClaw compatible)
- `skillbridge_test.go` — parse real SKILL.md examples, test prompt injection format

**SKILL.md format (must be compatible with OpenClaw and PicoClaw):**
```markdown
---
name: my-skill
description: Does something useful
version: "1.0"
tags: [tag1, tag2]
---
# Skill body
Instructions for the agent...
```

**Rules:**
- Prompt injection format must match OpenClaw's XML format exactly (see CLAUDE.md)
- `Install()` always runs seccheck first if `config.Skills.AutoSecCheck = true`
- Skill body is injected verbatim — no modification
- Skills directory: `~/.famclaw/skills/` or configured path

**Done when:** `go test ./internal/skillbridge/... -v` passes

---

## Agent 6 — mcp

**Scope:** `internal/mcp/`

**Uses:** `github.com/mark3labs/mcp-go` — official Go MCP SDK

**Deliverables:**
- `client.go` — `Client`: wraps mcp-go stdio client, `CallTool(ctx, name, args) (*mcp.CallToolResult, error)`
- `pool.go` — `Pool`: manage N MCP clients (one per skill), lazy start, auto-restart
- `mcp_test.go` — in-process mock MCP server via mcp-go, test full tool call round-trip

**Agent patch:** Update `internal/agent/agent.go`:
- After LLM responds, check if response contains tool_call
- Look up tool in mcp.Pool
- Execute tool, append result to messages
- Loop until no more tool calls (max 10 iterations)

**Rules:**
- MCP server processes are started lazily on first tool call
- Crashed MCP server is restarted once automatically
- Tool call loop has hard limit of 10 iterations — prevents infinite loops
- All tool calls are logged for the parent dashboard audit trail

**Done when:** `go test ./internal/mcp/... -v` passes

---

## Agent 7 — integration tests

**Scope:** `integration_test.go` (root level, build tag `integration`)

**Depends on:** all previous agents done

**Deliverables:**
- Full message flow test: unknown user → onboarding
- Full message flow test: child → allowed topic → LLM response
- Full message flow test: child → blocked topic → block message, LLM never called
- Full message flow test: child → approval topic → parent notified, LLM never called
- Full message flow test: parent → any topic → LLM called
- Gateway router test: same message via mock-Telegram and mock-Discord → identical policy outcome
- Cross-compile test: `make cross` completes without error

**Done when:** `go test -tags integration ./... -v` passes

---

## Agent 8 — seccheck skill (famclaw/skills repo)

**Scope:** produces files for `github.com/famclaw/skills` — a SEPARATE repo.
Output these files to `skills-repo/seccheck/` directory for manual copy.

**Deliverables:**
- `skills-repo/seccheck/SKILL.md` — AgentSkills compatible
  - triggers on: "check skill", "scan repo", "is this safe to install", "seccheck"
  - describes how to call the `seccheck` binary
  - works in famclaw (native), PicoClaw (SKILL.md only), OpenClaw (SKILL.md + binary)
- `skills-repo/seccheck/bin/main.go` — thin CLI wrapper around `internal/seccheck`
  - reads repo URL from args, runs scan, prints report, exits 1 on FAIL
  - builds to a static binary: `seccheck-linux-arm64`, `seccheck-linux-armv7`, `seccheck-linux-amd64`
- `skills-repo/seccheck/README.md` — usage, installation for each runtime
- Makefile target: `make build-seccheck` — builds all three binaries to `skills-repo/seccheck/bin/`

**SKILL.md frontmatter must include:**
```yaml
---
name: seccheck
description: Scan a skill or MCP git repository for security issues before installing. Checks for secrets, malicious network calls, CVEs, typosquatting, and runs in a sandbox.
version: "1.0"
author: famclaw
tags: [security, skills, mcp]
platforms: [linux, darwin]
requires:
  bins: [seccheck]
---
```

**Done when:** SKILL.md parses correctly, binary builds for all three targets

---

## Agent 9 — SD image

**Scope:** `scripts/`, `.github/workflows/`

**Deliverables:**
- `scripts/build-image.sh` — builds flashable .img from Raspberry Pi OS Lite base
- `scripts/firstboot.sh` — first boot: expand FS, set hostname, generate secret, prompt for config
- `scripts/firstboot-wizard.sh` — interactive first-boot setup wizard (which users, which gateways)
- `.github/workflows/ci.yml` — on every PR: test + cross-compile
- `.github/workflows/release.yml` — on tag: cross-compile all targets + build SD images + attach to GitHub Release
- `docs/FLASH.md` — step by step: download image → flash with Raspberry Pi Imager → boot → open famclaw.local

**Image contents:**
```
Raspberry Pi OS Lite (64-bit for rpi4/5, 32-bit for rpi3)
+ famclaw binary at /usr/local/bin/famclaw
+ famclaw.service at /etc/systemd/system/
+ default config.yaml at /opt/famclaw/
+ firstboot.service (runs wizard on first boot, disables itself)
+ Ollama NOT included (too large for image — pulled on first boot)
```

**Done when:**
- CI workflow runs clean on a test PR
- `scripts/build-image.sh` produces a bootable .img (tested in QEMU or real hardware)

---

## Agent 10 — docs

**Scope:** `docs/`

**Deliverables:**
- `docs/FLASH.md` — flash SD card, first boot, connect to famclaw.local
- `docs/GATEWAYS.md` — create Telegram/WhatsApp/Discord bots, add tokens to config
- `docs/PERSONAS.md` — add family members, link gateway accounts
- `docs/SKILLS.md` — install skills, write a skill, seccheck
- `docs/HARDWARE.md` — RPi model recommendations, model selection by RAM
- `docs/ANDROID.md` — Termux setup

**Done when:** all docs render correctly in GitHub markdown preview

---

## Agent 11 — supply chain security

**Scope:** `.github/workflows/`, repo root config files

**Can run in parallel with any wave** — no code dependencies, only CI/CD config.

**Deliverables:**
- `.github/workflows/ci.yml` updates — add `govulncheck`, `gosec`, dependency review on PRs
- `.github/workflows/release.yml` updates — add SLSA provenance attestation, SBOM generation, cosign signing
- `.github/dependabot.yml` — automated dependency update PRs
- `.github/workflows/codeql.yml` — CodeQL static analysis (Go)
- `SECURITY.md` — vulnerability reporting policy

**CI pipeline additions:**
- `govulncheck ./...` — Go vulnerability database check (blocks merge on known CVEs)
- `gosec ./...` — Go security linter (SAST)
- `actions/dependency-review-action` — flag new vulnerabilities introduced by PRs

**Release pipeline additions:**
- `actions/attest-build-provenance` — SLSA Build L3 provenance for every binary
- `anchore/sbom-action` — generate CycloneDX SBOM attached to GitHub Release
- `sigstore/cosign-installer` + `cosign sign-blob` — keyless signing of release binaries

**Rules:**
- Security checks MUST NOT break the build on first run — use warn mode initially, then enforce
- `govulncheck` blocks merge (hard gate)
- `gosec` warns but does not block (too many false positives on new codebases)
- SBOM and attestation are attached to GitHub Releases, not blocking
- SECURITY.md follows GitHub's recommended format

**Done when:**
- `govulncheck ./...` runs in CI and passes
- Release workflow generates attestation + SBOM
- Dependabot config is active
- SECURITY.md exists

---

## Parallel execution strategy for Claude Code

```
# Wave 1 — run in parallel (no dependencies)
agent-1-classifier  &
agent-2-policy      &
agent-3a-notify     &
agent-3b-identity   &
wait

# Wave 2 — run after wave 1
agent-4-gateway     # sequential, depends on all wave 1

# Wave 3 — run in parallel after gateway
agent-5-skillbridge &
agent-6-mcp         &
wait

# Wave 4
agent-7-integration-tests  # sequential, depends on all

# Wave 5 — run in parallel
agent-8-seccheck-skill  &
agent-9-sd-image        &
agent-10-docs           &
wait

# Agent 11 (supply chain security) can run in parallel with ANY wave
agent-11-security &
```

---

## Definition of done for the whole project

- [ ] `go build ./...` — clean, no errors
- [ ] `go test ./...` — all pass
- [ ] `opa test ./policies/` — all pass
- [ ] `make cross` — all 6 targets compile cleanly (CGO_ENABLED=0)
- [ ] `go vet ./...` — clean
- [ ] Integration test: child message never reaches LLM when blocked
- [ ] Flashable .img boots and serves famclaw.local:8080
- [ ] Telegram bot responds with correct policy decisions
