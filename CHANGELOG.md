# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

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

## v0.7.0 — 2026-07-23

### Added
- **Config hot-reload.** `config.yaml` is watched with an fsnotify file
  watcher; on a write that revalidates cleanly, the router, web server, and
  MCP pool configs are updated in place under their respective mutexes —
  no restart required. The watcher goroutine stops cleanly on shutdown via
  the gateway context. (#261)
- **MCP server management web API.** Three new session-authenticated endpoints
  manage configured MCP servers: `GET /api/mcp` (list), `POST /api/mcp/add`,
  and `POST /api/mcp/remove` (duplicate names rejected with 400). (#258)
- **MCP server management chat commands.** `.mcp list` / `.mcp add` /
  `.mcp remove` chat commands, parent-gated and persisted, in the gateway
  router. (#231)
- **Async research dispatch.** `spawn_agent` research jobs can run
  asynchronously and deliver their result back into the originating
  conversation; the delivery send is bounded by a timeout and correctly
  classified. (#253)
- **Per-user / per-group file-tool sandbox isolation.** Each user/group gets
  a confined sandbox root for `file_*` tools and the MCP child cwd, with
  containment asserted and the sandbox identity allowlisted. (#250, #254)
- **Explicit unsandboxed MCP opt-in (macOS).** `tools.sandbox.allow_unconfined:
  true` runs MCP servers unsandboxed where OS sandboxing is unavailable
  (e.g. macOS). Secure-by-default (fail-closed) with a loud startup warning
  and an actionable error. (#252)
- **Web_fetch browser fallback for JS-heavy sites.** When HTML→text
  extraction yields too little content, `web_fetch` falls back to a
  Playwright browser (reusing the browser tool's host allowlist) with
  graceful degradation and post-redirect host re-checking. (#260)
- **Image understanding for Discord.** Discord image attachments are
  decoded and sent to the LLM alongside text, matching the Telegram image
  path. (#236)
- **Telegram image downscaling.** Large Telegram images are downscaled
  before reaching the LLM via the new `imageutil` package (WebP/GIF-aware,
  Catmull-Rom), preserving PNG transparency and passing unsupported formats
  through unchanged. (#235, #225)
- **Discord file attachment persistence.** Discord message attachments are
  persisted to the agent sandbox with extension-MIME consistency, a size
  cap, path-traversal-safe writes, and secure directory permissions. (#237)
- **macOS checksum verification in update.sh.** The update script now
  verifies release checksums on macOS. (#223)
- **Web search hinted in the system prompt.** When `web_search` is registered
  for a user, the PromptBuilder's capabilities section names it with a
  concrete usage hint, fixing the "I can't search the web" failure mode for
  equipped agents. (#262)

### Changed
- **Browser tool enabled by default.** The built-in `browser` tool is now
  enabled by default. (#248)

### Fixed
- **Serialized WebSocket writes per connection.** WebSocket writes are
  serialized per connection, fixing a data race on concurrent writes. (#264)
- **Per-user memory cap.** User memories are capped with oldest-eviction on
  upsert, preventing unbounded growth. (#263)
- **Async delivery timeout.** Async research-result delivery send is bounded
  by a timeout. (#256)
- **Control-character rejection in user memories.** Control characters are
  rejected in `remember` fields. (#259)
- **GIF/PNG decoder registration.** All accepted image MIME types, including
  GIF and PNG, now decode cleanly. (#255)
- **Honest web_fetch errors.** `web_fetch` returns the actual fetch error
  instead of confabulating a response. (#238)
- **Multilingual false-completion detection.** The stage-tool-loop
  false-completion detector triggers only on explicit success phrases, now
  covering multilingual success claims instead of a catch-all. (#249)
- **Web search disabled-state message.** The `web_search` disabled-state
  message now gives generic guidance instead of a tool-specific hint. (#243)
- **False tool-completion claims neutralized.** The stage-tool-loop no longer
  emits false tool-completion claims; success matching is explicit and
  multilingual. (#240)
- **Role-allowed tools always advertised.** Role-allowed tools are now always
  advertised in prompts. (#230)
- **Context-aware history compression.** The initial LLM call uses
  context-aware history compression. (#226)
- **sandbox_root validation errors.** Invalid `sandbox_root` config now
  includes the offending path in validation error messages. (#234, #222)
- **Portable checksum verification.** Update-script checksum verification
  works on both GNU and BSD systems. (#233)

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
