# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## v0.9.0 — 2026-08-01

### Added
- **File tools for children.** Children can now use file read/write tools
  (previously restricted to parents only) after approval.
- **Vision+tools two-step description.** Image attachments are now processed
  in two steps: first the image is described, then the agent acts on the
  description (e.g., photo of an item → add to inventory, photo of an event
  → add to calendar).

### Changed
- **Empty non-assistant messages now include content key.** Empty non-assistant
  messages now properly include a `content` key, which fixes compatibility
  with llama.cpp backends that previously rejected such messages with HTTP 400.

### Fixed
- **LLM content key consistency.** Fixed an issue where empty non-assistant
  messages lacked the `content` key, causing compatibility problems with
  llama.cpp-based local models.

## v0.8.0 — 2026-07-30

### Added
- **Image attachments forwarded to the model.** Photos sent on Telegram and
  Discord are now passed through to the LLM as `image_url` content parts
  (multimodal), instead of being dropped with an "I don't see any image
  attached" reply. Messages carrying attachments route to a configurable
  `llm.vision_profile` LLM endpoint; text-only messages stay on the normal
  per-user endpoint. The Claude CLI backend is vision-capable by default.
- **`/api/health` reports boot-skipped MCP servers.** The health endpoint now
  includes an `mcp_skipped` array listing MCP servers that failed to start at
  boot, so operators can see which servers are unavailable without inspecting
  logs.
- **Searchable user memories.** Recall from the `remember` tool can now be
  filtered by keyword: case-insensitive substring matching across the memory's
  label, value, and category, scoped per-user. Special LIKE characters
  (`%`, `_`, `\`) are matched literally.
- **web_fetch browser fallback for JS-heavy pages.** When HTML-to-text
  extraction yields too little content, `web_fetch` can fall back to a
  Playwright browser render of the page. Opt-in via
  `tools.web_fetch.fallback_to_browser` (off by default); requires
  `tools.browser.enabled`. Each failure mode — pool unavailable, browser
  error, empty render — returns a distinct error rather than a silent empty
  result.

### Changed
- **Family facts and per-user memories are now injected into the system
  prompt.** The `prompt.BuildContext` `FamilyState` and `UserMemory` fields
  existed but were never populated, so safety-critical family facts (allergies,
  dietary restrictions) were not auto-injected via the `<family_safety>` block.
  Both are now built every turn and passed into both the default and the
  operator-override (`cfg.LLM.SystemPrompt`) prompt paths. A snapshot read
  failure is logged and downgraded to an empty injection so a database hiccup
  never breaks the turn.
- **Tool definitions use bare names.** Tool definitions sent to the LLM now use
  the bare name (`web_search`) advertised in the capabilities prompt, instead
  of the namespaced `builtin__` form that strict backends (e.g. vLLM)
  rejected. MCP tool names (`mcp__<server>__<tool>`) are unchanged.
- **"No action was completed" note is language-agnostic.** The note appended
  when every tool call in a turn failed now fires on the tool loop's exit
  reason — the model produced a final answer with no further tool calls — rather
  than a curated list of success phrases, so it works in any language and does
  not misfire when the iteration cap is exhausted mid-tool-calls.
- **Grounding rule directs the model to try tools first.** The "current/live
  information" behavioral rule was reworded so the model always attempts its
  search/fetch tools before reporting it lacks current data, reserving that
  fallback for genuinely toolless cases.

### Fixed
- **Tool-call arguments serialized as a JSON string.** Tool-call arguments are
  now marshaled as a JSON string (per the OpenAI spec) instead of a JSON
  object. The previous object encoding was rejected with HTTP 400 by strict
  backends such as vLLM, which broke tool use entirely.
- **Telegram captionless photos no longer dropped.** The polling loop
  previously skipped any message with an empty `Text` field, dropping photos
  sent without a caption. Photo-only messages are now forwarded to the agent
  as image attachments.

### Dependencies
- Bumped `github.com/mark3labs/mcp-go` from 0.56.0 to 0.57.0.
- Bumped `actions/checkout` from v7.0.0 to v7.0.1 (CI).

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
  startup without a repo clone for the `internal/policy/policies/` directory. Policies
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