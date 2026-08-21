# Changelog

All notable changes to FamClaw are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## v0.13.0 — 2026-08-21

### Changed
- **macOS release binaries are now Developer ID-signed and notarized (#311, #214).** Release darwin binaries are signed with a "Developer ID Application" certificate, submitted to Apple's notary service, and stapled during the release build (`scripts/darwin-sign-notarize.sh`, driven by `FAMCLAW_APPLE_*` / `FAMCLAW_NOTARY_*` credentials — see `docs/RELEASE.md`). The release fails loudly with an actionable message when those credentials are missing: an unsigned or ad-hoc signed arm64 binary is killed on launch (exit 137), and a non-notarized binary is blocked by Gatekeeper after a browser download. The previous ad-hoc-only signing is gone. Upgrades keep the atomic-rename install path.
- **Reminders and cross-user messages resolve targets to canonical names (#365).** The model addresses family members by display name or any case variant ("Julia", "Dep"), while the store keys on the lowercase config `Name`. `add_reminder` and `send_message` now normalize the target at the tool boundary (`Config.CanonicalName`: exact `Name`, then case-insensitive `Name`, then case-insensitive `DisplayName`), so a linked account is found instead of failing with "X has no linked gateway account" even though `list_users` showed it linked.

### Fixed
- **`add_reminder` honors explicit times for multi-day offsets (#366).** "in 2 days at 17:00" (or "in 2 days at 5 pm") now lands exactly at that time of day on the calendar day two days out — previously the explicit time was silently dropped and the reminder fired at the *current* time N days later. A bare "in N days" still reminds at the current time N days from now, and inputs that could not mean the requested time ("in 2 days at 13:00 pm") are rejected rather than scheduled at the wrong hour. The `add_reminder` tool description and `docs/FAMILY.md` document the accepted forms.
- **Reasoning vs. chain-of-thought handling is now model-aware and language-agnostic (#364).** FamClaw no longer decides "is this text the answer or internal reasoning?" by matching English deliberation phrases. Models are classified by their reasoning-field semantics: known thinking models (qwen3 family, nemotron, gpt-oss) never have reasoning hoisted into an empty `content`; answer-in-reasoning models (gemma-4-26b) and unknown models hoist with control-token stripping. During streaming, answer tokens stream live while reasoning fields are buffered in separate builders and reconciled at end of stream — no live CoT leak, no language bias (French/German/Chinese/Russian/Greek CoT is suppressed; legitimate non-English answers pass).
- **The macOS service install now matches the updater's plist name.** `make install-launchd` installed the launchd plist as `com.famclaw.plist` while `scripts/update.sh` looked for `com.famclaw.famclaw.plist` (the plist's own Label), so the documented Mac upgrade path failed with "launchd plist not found" on every `make`-installed machine. The Makefile now installs the label-named file (launchd convention: filename == Label), and the updater still finds legacy `com.famclaw.plist` installs and its error message tells you exactly how to fix either case.

### Dependencies
- Bumped `github.com/mark3labs/mcp-go` from 0.57.0 to 0.58.0, `golang.org/x/crypto` from 0.54.0 to 0.55.0, `golang.org/x/net` from 0.57.0 to 0.58.0, `golang.org/x/image` from 0.44.0 to 0.45.0, and `golang.org/x/text` from 0.40.0 to 0.41.0 (go-minor-patch group).

## v0.12.0 — 2026-08-14

### Added
- **Parents can manage MCP servers from within a chat.** The parent LLM now has two new built-in tools: `mcp_list` lists every configured MCP server with its transport (stdio, HTTP, or SSE) and live load status (loaded, error, or not-yet-loaded); `mcp_add` registers a new server by validating the configuration, saving it to `config.yaml` under `skills.mcp_servers`, and reloading the running MCP pool. Both tools are parent-only — a child cannot add or inspect MCP servers. If a newly added server cannot be loaded by the running pool (for example, because the command does not exist), `mcp_add` reports the error and reassures you that the server is saved and will load on restart, rather than silently claiming success. This complements the parent dashboard's "MCP Servers" panel for the same management from the web UI.

### Changed
- **Voice messages now go through the same approval gate as typed text.** v0.11.0 added voice transcription, but it ran inside the agent's pipeline — after the router had already classified and policy-checked the message on its *empty* text. That meant a child's spoken request could slip past the approval flow: the router saw an empty string (allowed) and only the agent ever heard the transcript, so a parent would never be notified for an age-blocked or sensitive spoken request. FamClaw now transcribes audio attachments in the router, *before* the classifier and policy evaluation, so a spoken request receives exactly the same age/approval gating as if it were typed. Transcription failure or a disabled transcriber returns a visible "voice isn't available" reply — never silence.
- **Config hot-reload now reports the truth per component.** When `config.yaml` is reloaded, FamClaw walks every component through a reload registry and logs each one's actual outcome. Reloadable components (including the voice transcriber, which is now hot-reloadable) report success or failure; components that cannot be hot-reloaded report "requires restart" instead of being silently skipped. Previously, the reload handler sometimes reported a blanket success even when individual components had silently failed. See *Configuration hot-reload* in `docs/AGENT_SETUP.md`.
- **`web_search` startup probe now tells a slow endpoint from a dead one.** The startup reachability probe now uses a 30-second timeout (matching the web search request timeout) and reports "timed out" for a slow-but-listening SearXNG instance versus "unreachable" for a genuine connection failure. Previously, the 5-second probe falsely flagged slow self-hosted search backends as unreachable in the startup log.
- **The tool-result spillover cache now actually engages.** The head-budget threshold was lowered from 50% of the context window (~213 KB at the default 128K-token context, which no real tool result ever exceeded) to 2% (~8.5 KB at 128K tokens), with an absolute floor of 512 B and ceiling of 64 KB. Realistic web fetches (10–30 KB), file reads, and search results now spill to the per-user cache as intended; small inline results stay inline.
- **Proactive messages now respect which gateways can actually be started.** When FamClaw or a reminder proactively messages a family member, it now distinguishes whether each linked gateway can be initiated: Discord is always initiable (a bot can open a DM to a shared-guild member), but Telegram is initiable only after the user has sent the bot at least one message (Telegram forbids bots from starting conversations). If no linked gateway can be initiated, the error message names the person, lists the linked gateways, explains why each cannot be started, and states what unblocks delivery — instead of failing opaquely inside the platform's send call.
- **Reminders understand natural-language durations.** `add_reminder` now accepts phrases people actually type: "in 1 min", "in 20 seconds", "in 2 hrs", "in half an hour", "in 3 days", "tomorrow morning". Bare units without an amount (e.g. "in minute", "in day") are rejected with examples of valid formats.

### Fixed
- **Skill security scanning now actually runs.** `seccheck.enabled: true` was previously the master switch in documentation only: when `auto_seccheck` was unset (it defaults to false), the install-time and CLI scan gates were skipped, so skills were installed unscanned whenever the combination `Enabled && AutoSecCheck` was false — even when the HoneyBadger scanner was available. FamClaw now gates scanning on `seccheck.enabled` alone, so every skill install is scanned when security checking is enabled; a missing/unavailable scanner refuses the install (fail-closed) with a clear error. FamClaw also auto-fetches the HoneyBadger binary at startup if it is missing (`EnsureScanner`), scans pre-installed skills at boot (skipping only those already scanned within the rescan interval), quarantines failed runtime tools, and surfaces the scanner state through `/api/health`.
- **The vision "describe" step now gives an honest error.** When an image is attached but the vision system is not configured or the describe step fails, the fallback note now explains the actual reason ("the vision system is not configured or the describe step failed") instead of the misleading "I could not read the image you attached."
- **`web_fetch` now logs when the browser fallback can't fire.** When `tools.web_fetch.fallback_to_browser` is enabled but no browser pool is configured (or it failed to initialize), `web_fetch` now logs a visible warning and degrades to the plain fetch text — so you can see the missed fallback rather than silently getting thin content that the assistant might explain away. The config comment and README now state plainly that the flag requires a reachable `tools.browser.endpoint`, not just the flag.
- **Empty voice attachments are rejected before transcription.** A zero-byte audio attachment is now caught early and returns a visible "voice isn't available" reply, instead of being silently dropped or sent as empty bytes to the transcription service.
- **Binary installs no longer crash with exit 137.** The `install-rpi.sh` script, `make install`, and the Android install path now download to a temporary file in the same directory as the destination and atomically rename it into place. This fixes the SIGKILL / exit-code-137 crash that occurred when an upgrade overwrote a running binary in place (the kernel refused to re-exec the modified inode). The RPi installer was also fixed to stop aborting entirely when internal variables were unbound under `set -euo pipefail`.
- **Research tasks no longer fail instantly and report a fake timeout.** Asking the assistant to research something used to fail in about one second but be reported as "timed out after 300 seconds" — a five-minute failure that never actually happened. The research task inherited the chat turn's lifetime and was killed the moment your reply was sent, and any fast failure was re-labelled as a timeout. Research tasks now run independently of the chat turn but are still cancelled properly when the service shuts down, so a task can no longer be orphaned; saving and delivering a result is time-bounded rather than able to hang; and a cancelled or failed task is reported with its real reason instead of a fabricated timeout.

### Dependencies
- Bumped `modernc.org/sqlite` (go-minor-patch group).
- Bumped `actions/attest-build-provenance` (CI).

## v0.11.2 — 2026-08-06

### Fixed
- **Reminders are now actually delivered.** v0.11.0 added proactive family reminders, but the delivery path was broken in three ways: FamClaw resolved a family member's destination from past message activity instead of their linked `gateway_accounts`, so anyone who had been set up on a gateway but never messaged the bot was silently reported as unreachable; Discord reminders were sent to a Discord user ID where a channel ID was required, producing a permanent "Unknown Channel" 404 on every delivery attempt; and a reminder whose delivery failed was never marked as dispatched, so it was retried forever in a 30-second loop. FamClaw now resolves destinations from `gateway_accounts` (the authoritative reach record) and only uses recent message activity to prefer one gateway when several are linked — a linked-but-silent family member is always reachable; Discord reminders open (or reuse, cached) a DM channel and send to that channel; and delivery is capped at 3 attempts (`MaxDeliveryAttempts`) before the reminder is given up, so a broken destination stops hammering instead of looping forever. A family member with no linked account at all is honestly reported as unreachable.

## v0.11.1 — 2026-08-05

### Fixed
- **Conversation memory is restored.** v0.11.0 introduced a regression that broke conversation continuity: every user message was treated as the start of a brand-new conversation, so the assistant forgot everything between turns and the family lost their chat history mid-conversation (measured at roughly 2 messages per conversation on affected databases, down from the normal 30+). FamClaw now reuses the existing conversation ID verbatim for the life of a conversation, so messages keep landing in the same conversation as long as the gap between the user's last message and now is under the 6-hour `ConversationIdleTimeout`; only a longer idle gap or a cold start begins a fresh conversation. Existing conversations and their history are preserved.

## v0.11.0 — 2026-08-04

### Added
- **Voice message transcription on Telegram and Discord.** Voice notes and audio clips sent to the bot are now transcribed into text by a local speech-to-text service and handled exactly like a typed message — they pass through the same age/approval policy before reaching the LLM. This closes the previous "voice is not supported" gap where a voice message was silently dropped with no reply; when transcription is not configured, the assistant now returns a visible "voice isn't available" notice instead. Enable it under `tools.transcription` (point `endpoint` at your `/v1/audio/transcriptions` service and set `model`; default `max_bytes` is 25 MB / 26,214,400 bytes, default `timeout_seconds` is 30).
- **Proactive family reminders.** A family member can ask the assistant to be reminded about something at a specific time ("remind me to take out the trash in 2 hours" / "remind me tomorrow at 9am"), and FamClaw delivers the reminder automatically at that time — the recipient does not need to send another message first. Reminders are delivered through the recipient's most recently used messaging app. Parents can set reminders for other family members ("remind Emma to come downstairs"); children can only remind themselves. Backed by the `builtin__add_reminder` tool.
- **The assistant can message another family member.** On its own initiative, the assistant can send a message to another family member on their messaging platform — for example when a reminder fires for someone else, or when the assistant decides a note belongs to a different user. Only configured family members are addressable; if a family member hasn't messaged the bot yet, FamClaw reports that honestly rather than guessing a channel. Every proactive message is recorded in the audit log (sender, target, content) and also appears in the recipient's conversation history in the dashboard. Parent-only, via the `builtin__send_message` tool.
- **Messages now record which gateway they arrived on.** Each stored message keeps the gateway it came from (Telegram, Discord, etc.), which is what lets reminders and cross-chat messages be delivered to the right platform. Existing messages keep a `unknown` sentinel; no data is lost.
- **The current date is injected into the system prompt.** The assistant now sees the host's current date and timezone in every system prompt, so it no longer invents wrong dates ("January 8, 2026" on an August run). This is on by default for all deployments.

### Changed
- **Conversations no longer reset at midnight.** The conversation ID used to be bucketed by calendar day, so every conversation silently restarted at midnight. FamClaw now keeps a conversation going as long as the gap since the user's last message is under 6 hours (the `ConversationIdleTimeout` constant); a longer gap or a cold start begins a fresh conversation.
- **`web_search` now fails honestly when the backend is unreachable.** If your SearXNG endpoint is down, timing out, or returning errors, the assistant reports that it could not search right now instead of silently returning nothing or inventing results. A zero-hit response is still a normal "no results," distinct from an unavailable backend. FamClaw also logs a startup `WARNING` if the configured endpoint cannot be reached, so a dead search backend is never silently "configured but dead." Endpoint credentials are stripped before logging.

### Fixed
- **Gemma control tokens no longer leak into replies.** The Gemma model format (`<|tool_call_begin|>` and friends) was reaching the family as visible text when tool calls were emitted inline or streamed. FamClaw now salvages native Gemma tool calls, strips every control token from both streamed and full responses (with a carry-over buffer for tokens split across SSE chunks), and returns an honest error instead of a blank reply when the model produces no content and no tool calls.
- **Repeated failed tool calls are short-circuited within a turn.** When a tool call (e.g. `web_search`) failed and the model re-emitted the identical call, the loop could re-execute the same doomed call and repeat the apology. FamClaw now detects an identical (same name + arguments) call that already failed earlier in the turn and feeds it back a corrective result instead of running it again. Calls with different arguments or from a different turn are unaffected.

### Dependencies
- Bumped `github.com/open-policy-agent/opa` from 1.18.2 to 1.19.0 and `modernc.org/sqlite` from 1.54.0 to 1.55.0 (go-minor-patch group).

## v0.10.0 — 2026-08-01

### Added
- **MCP Servers dashboard panel.** MCP tool servers (stdio, HTTP, or SSE) can now be added and removed from the parent dashboard instead of hand-editing `config.yaml` and restarting. The new "MCP Servers" panel lists each configured server with a Remove button and includes an add form for a name, transport, and transport-specific fields (command + args for stdio, URL for http/sse), with client-side validation before the server is saved.
- **File tools are now sandboxed per conversation.** `file_read`, `file_write`, `file_stat`, and `file_list` are now scoped to each conversation — a DM on Telegram, a channel or group on Discord — so a file saved in one conversation cannot be read by another. Each conversation gets its own file root (`conversations/<gateway>/<id>`), and the gateway name and identity are sanitized so a crafted value cannot escape the sandbox tree. Family facts and user memories remain shared across all conversations. Existing per-user/per-group files are migrated to `conversations/_legacy/` automatically on first startup.

### Changed
- **Web search timeout raised to 30s.** The default timeout for `web_search` was raised from 10s to 30s because a self-hosted SearXNG instance fanning out to multiple upstreams routinely exceeds 10s (a value tuned for a commercial search API). Override with `tools.web_search.timeout_seconds` in `config.yaml`.
- **Allowlist rejections are not network errors.** When a URL host is blocked by `tools.web_fetch.url_allowlist`, the error now names the host explicitly and makes it clear this is a configuration choice — not a transient network failure — and that a parent can add the host to `url_allowlist` to permit it. The same clear message is used across `web_fetch`, `web_search`, and the browser tool.
- **Tool-failure warning is now prepended.** When every tool call in a turn fails and the model produces a final answer, the warning is now placed at the top of the response (not buried in a trailing note) so you are never misled into thinking something was actually looked up. The model's answer is preserved — it just no longer masquerades as verified research.
- **Secret-resolution diagnostics no longer echo error detail.** Diagnostic log lines about failed secret resolution no longer include the raw error text — they point to the `[vault]` log lines above for specifics, keeping sensitive context out of the summary line.

### Fixed
- **macOS binaries now run on Apple Silicon.** The released `famclaw-darwin-*` binaries were killed instantly on launch (exit code 137, no output) on Apple Silicon because the cross-compiled binary carried no valid signature and the kernel refused to exec improperly-signed arm64 Mach-O images. Release builds now ad-hoc codesign darwin binaries on a macOS runner so they run out of the box on both Intel and Apple Silicon Macs. Linux binaries are unaffected.

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
# no-op
