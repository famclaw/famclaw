# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## Unreleased

### Changed
- **Agent constructor takes `AgentDeps` struct** instead of 7 individual
  setter methods. Forgotten dependencies now surface at compile time
  instead of as a runtime nil-pointer dereference.
- `MaxToolCallIterations` constant moved to the top of `internal/mcp/pool.go`
  with a godoc comment.
- `integration_test.go` moved from the repo root into `e2e/` as
  `package e2e` (kept `//go:build integration` tag — CI command unchanged).

### Fixed
- Database write errors (`SaveMessage`) are now logged instead of silently
  swallowed. Disk-full and schema corruption surface in the logs instead
  of being lost.

### Removed
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
