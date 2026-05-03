# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## Unreleased

### Added
- **Built-in `web_fetch` tool.** When `tools.web_fetch.enabled: true` in
  config, the LLM gets a `web_fetch` tool that retrieves a URL and
  returns extracted text (HTML→text via `golang.org/x/net/html`, plain
  text and JSON passed through). Defaults: 256 KB cap, 15 s timeout, no
  JS rendering, parent-only role gate, optional per-host allowlist with
  subdomain matching. Per-user allow/deny via OPA `tool_policy` rules
  (parent and `age_13_17` allowed; `under_8` and `age_8_12` denied).
  Closes journal critical finding #2 (partial — web search follows in a
  separate PR).
- **OPA tool-policy enforcement at the tool loop.** New
  `Evaluator.EvaluateToolCall` queries `data.family.tool_policy.allow`
  and the in-pipeline tool loop now gates every dispatch through it.
  Fails closed on evaluator errors. Tool names are stripped of their
  `builtin__` / `mcp__<server>__` prefix before evaluation so Rego rules
  match on the bare name.
- **PromptBuilder describes builtin tools.** When `web_fetch` or
  `spawn_agent` is registered for a user, the system prompt's
  capabilities section names them with concrete usage hints — fixes the
  "I can't fetch URLs" failure mode for tool-equipped agents.
- **Install and remove skills from the web dashboard.** Two new PIN-gated
  endpoints: `POST /api/skills/install` (body `{"name_or_path": "..."}`)
  wraps the existing `skillbridge.Registry.Install`, and
  `POST /api/skills/remove` (body `{"name": "..."}`) mirrors it. The
  dashboard's Skills card gets a one-line install form and a 🗑️ button
  per installed skill. `/api/skills` now reads from the on-disk registry
  (the previous DB-backed list was always empty because nothing wrote to
  it). Closes journal critical finding #6.

### Changed
- `prompt.BuildContext` gains a `BuiltinTools []string` field; `Agent`
  threads the bare names of builtins it has registered for the current
  user (filtered by role) into `prompt.Build`.
- The agent's builtin-handler dispatch is no longer gated on the
  presence of the subagent scheduler — it now activates whenever any
  builtin tool is registered. `handleSpawnAgent` returns a clear error
  if invoked without a scheduler.

### Removed
- **Hardcoded keyword block** of `web_search` / `mcp__search__web` in
  `internal/agentcore/stage_policy_tool.go` — superseded by the OPA
  tool-policy decision wired into the tool loop.

- **Unknown-accounts backend (issue #111).** New `unknown_accounts` table
  records every unlinked Discord/Telegram account that messages FamClaw,
  with attempts counter and last-seen timestamp. Three new PIN-gated JSON
  endpoints expose it: `GET /api/unknown-accounts`,
  `POST /api/unknown-accounts/link`, `POST /api/unknown-accounts/dismiss`.
  The router auto-clears rows on every link path (display-name auto-link,
  numbered-list reply, web link). Dashboard UI lands in a follow-up PR.
- **Gateway self-registration.** New users messaging FamClaw on Telegram or
  Discord are auto-linked when their platform display name matches an
  unlinked FamClaw user. When multiple unlinked users exist and no name
  match, a numbered list lets the user pick. Unknown accounts with no
  unlinked users are rejected — account creation is parents-only.
- **Bot setup wizard with token testing.** Step-by-step instructions for
  creating Telegram and Discord bots. Tokens verified against the platform
  API before saving. Discord OAuth2 invite URL auto-generated with the
  minimum required permissions (Send Messages + View Channel + Read
  Message History = 68608).
- `DisplayName` field on `gateway.Message`, populated from Telegram
  `FirstName`/`LastName` and Discord `GlobalName`/`Username`.
- `internal/gateway/chunk.go` — `ChunkMessage(text, maxLen)` utility for
  platform character limits, with table-driven tests.
- `UnlinkedUsers` method on identity store, `HasGatewayAccount` helper on
  the db store.
- API endpoints `/api/setup/test-telegram` and `/api/setup/test-discord`.
- `internal/web/settings_test.go` covering the four PIN scenarios that
  #109 broke (true first boot, re-run with correct PIN, wrong PIN, no PIN).

### Changed
- **System prompt rebuilt as a 12-component PromptBuilder** (`internal/prompt`).
  The default system prompt was a single sentence (`"You are FamClaw, a
  helpful, friendly, and safe family AI assistant."`), which caused real
  failures in the field — the deployed model told a parent *"I can't
  execute code"* despite having tools. The new builder assembles identity,
  user, family, age, capabilities, skills, policy, approvals, gateway,
  output, memory (placeholder), and OAuth-prefix components. Each is
  individually conditional. Token budget regression tests guard the size
  (parent ≤ 1100 tokens, child ≤ 750). Operator-supplied
  `cfg.llm.system_prompt` keeps legacy behavior verbatim — no breaking
  change for customized deployments.
- **Agent constructor takes `AgentDeps` struct** instead of 7 individual
  setter methods. Forgotten dependencies now surface at compile time
  instead of as a runtime nil-pointer dereference.
- `MaxToolCallIterations` constant moved to the top of `internal/mcp/pool.go`
  with a godoc comment.
- `integration_test.go` moved from the repo root into `e2e/` as
  `package e2e` (kept `//go:build integration` tag — CI command unchanged).
- Onboarding flow auto-matches platform profiles or shows a numbered list.
  Strangers no longer auto-create users.

### Fixed
- **Wizard "Finish setup" no longer fails with 403 on re-run.** Wizard now
  sends the parent PIN from the family-member step. PIN-mismatch shows a
  clear error ("Incorrect parent PIN. If re-running setup, use the PIN
  from your first setup.") rather than the generic failure toast. Fixes #109.
- **Discord messages over 2000 chars no longer silently dropped** — split
  into multiple messages at newline boundaries.
- **Telegram messages over 4096 bytes no longer silently dropped.** Telegram
  `sendMessage` now uses a JSON POST body instead of URL query parameters
  (the old form hit URL length limits well before the 4096-byte cap).
- Telegram `tgUser` parser now captures `FirstName`/`LastName`/`Username`
  (previously only `ID` was decoded).
- Empty / whitespace-only LLM replies are no longer sent to platforms
  (both rejected them with 4xx, leaving the user with silent failure).
- Database write errors (`SaveMessage`) are now logged instead of silently
  swallowed. Disk-full and schema corruption surface in the logs instead
  of being lost.

### Removed
- **mDNS removed entirely.** `famclaw.local` didn't resolve reliably on
  Windows or many home routers — use the device's IP address. Closes #110.
  The `grandcat/zeroconf` dependency, `internal/mdns` package,
  `scripts/install-termux.sh`, and the Android binary in GoReleaser have
  all been dropped.
- `Server.MDNSName` config field is retained for compat but marked
  deprecated. Notification approval URLs still consume it — set it to your
  device's IP or DNS hostname so the URLs work for recipients off the LAN.
- `min(a, b int)` shim — Go 1.21+ provides a builtin `min`.
- `outputBlockedPatterns` and `filterOutput` dead code in `internal/agent`
  (production filtering lives in `internal/agentcore/stage_output_filter.go`,
  covered by `TestStageOutputFilterChild`).
- `Config.LLMClientFor` — duplicate of `LLMEndpointFor` with no callers.
- `SecCheckConfig.{Sandbox, Timeout, OSVAPI}` legacy fields — never read.

## v0.5.0 — 2026-05-01

### Added
- **Agent dispatch via `spawn_agent` builtin tool.** The parent LLM can
  delegate sub-tasks to a different LLM profile by calling
  `spawn_agent(prompt, profile)`. The subagent runs on the specified
  profile (e.g., Qwen3-14B on local Ollama), has access to explicitly
  allowed MCP tools, and returns its result to the parent conversation.
  Concurrency controlled by the subagent scheduler (default: 2
  concurrent). Configurable timeout via `timeout_seconds` (default 5
  minutes). Parent-only role gate.
- `LLMEndpointForProfile(name)` config helper for direct profile
  resolution by name.
- `BuiltinHandler` support in the agentcore tool loop — builtin tools
  (prefixed `builtin__`) route to a handler function instead of the
  MCP pool.
- README section documenting `spawn_agent` dispatch, JSON schema, and
  subagent guarantees.

### Fixed
- **OPA policies embedded in binary.** Previous releases crashed at
  startup without a repo clone for the `policies/` directory. Policies
  now ship inside the binary via `go:embed`. Custom overrides still
  supported via `policies.dir` and `policies.data_dir` in config.yaml.
- **Half-overridden policy bundles rejected.** Setting only
  `policies.dir` without `policies.data_dir` (or vice versa) previously
  mixed embedded and filesystem sources silently. Now fails fast with a
  clear error.
- **Scheduler concurrency race fixed.** `Submit()` previously checked
  and acquired the concurrency slot without holding the lock (TOCTOU).
- **Subagent results no longer cross-delivered.** Each `Submit()` now
  returns a per-call result channel instead of a shared channel.
- **Sub-second `timeout_seconds` handled correctly.** Values like 0.5
  previously truncated to 0, creating an immediate-deadline subagent.
- `tool_call_id` propagated on `llm.Message` for OpenAI-compatible API
  compliance. Test coverage added for all four tool-reply branches.

### Changed
- Production `.rego` files and `topics.json` moved from `policies/`
  (repo root) to `internal/policy/policies/`. OPA test commands updated.
- Subagent tools default to deny — empty allowlist means no MCP tools.
  Parent must explicitly grant tool access via `tools` parameter.
- Startup log distinguishes `Policies: embedded (built-in)` from
  `Policies: <path> (custom override)`.
- CI: OPA install pinned to v1.15.2 from GitHub Releases with SHA256
  verification, retry/timeout, and binary caching.

### Upgrade notes
- If you have a `policies:` block in your config.yaml from an earlier
  install, remove it so the binary uses embedded policies:
  ```bash
  sudo sed -i '/^policies:/,/^$/d' /opt/famclaw/config.yaml
  ```

## v0.4.0 — 2026-04-07

Initial release with runtime security scanning, install-time skill
gating, OPA content filtering, Telegram/Discord/WhatsApp gateways,
multi-format skill adapters, and inference sidecar.
