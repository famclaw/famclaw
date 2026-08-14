## FamClaw — Agent Instructions

## Codebase orientation

FamClaw is a secure, local-first family AI gateway that connects family members to AI models through Telegram, Discord, and a web interface. Every message passes through a policy engine before reaching the LLM, ensuring age-appropriate responses and parental approval for sensitive topics. It runs on Raspberry Pi, Mac, or any Linux box, using a single CGO_ENABLED=0 binary.

### Runtime shape

The process starts with `cmd/famclaw/main.go`, which loads configuration from the path specified by `--config` (default `config.yaml`), initializes the database, and starts gateways (Telegram, Discord) and the web server. The gateway router (`internal/gateway/router.go`) handles incoming messages, resolves identities, and routes them through the policy pipeline. The web server (`internal/web/server.go`) serves the UI and REST/WebSocket API. All components are wired through dependency injection, with the `main.go` file orchestrating startup and shutdown.

### Package map (internal/)

| Package | Responsibility | Key types / entry points | Depends on (other internal/) |
|---|---|---|---|
| config | Configuration loading and validation | `Config`, `Load`, `Validate` | - |
| gateway | Message routing and identity resolution | `Router`, `Handle`, `NewRouter` | config, identity, policy, store, notify, classifier |
| web | HTTP server, web UI, WebSocket API | `Server`, `Handler`, `NewServer` | config, store, identity, policy, notify, skillbridge, mcp, classifier |
| store | SQLite database access and migrations | `DB`, `Open`, `migrate` | - |
| policy | OPA policy evaluation for input, tool calls, and output | `Evaluator`, `Evaluate`, `EvaluateOutput` | - |
| notify | Approval notifications via gateway senders | `MultiNotifier`, `Notify`, `GenerateToken` | config, identity, store |
| identity | User identity and account linking | `Store`, `Resolve`, `LinkAccount` | store |
| agent | Core AI chat and pipeline execution | `Agent`, `Chat`, `NewAgent` | agentcore, browser, classifier, compress, config, familystate, gateway, llm, mcp, policy, prompt, reminder, skillbridge, store, subagent, todo, toolcache, usermemory, webfetch, websearch |
| agentcore | Composable pipeline stages (classify → policy → LLM → tools → output) | `Pipeline`, `Stage`, `Turn` | classifier, compress, config, honeybadger, llm, mcp, policy, skillbridge, store |
| subagent | Agent dispatch and sub-agent scheduling | `Scheduler`, `Submit`, `NewScheduler` | mcp |
| toolcache | Tool result spillover cache with TTL and eviction | `Cache`, `New`, `StartSweeper` | store |
| compress | Context-window compression for chat message lists | `Compress`, `Message`, `Options` | - |
| browser | Browser navigation and screenshot tools | `Pool`, `NewPool`, `Tools` | - |
| webfetch | Web fetch tool with URL allowlist and SSRF guards | `Tool`, `Fetch` | - |
| websearch | Web search tool with SearXNG integration | `Tool`, `Search` | - |
| filetool | Builtin file read/write/stat/list tools | `FileReadTool`, `FileWriteTool`, `FileStatTool`, `FileListTool` | agentcore |
| familystate | Shared family memory (allergies, dates, pets) | `Store`, `GetTool`, `ProposeTool` | store |
| usermemory | Per-user memories with searchable recall (remember/recall/forget) | `Store`, `Snapshot`, `SearchMemories` | agentcore, store |
| honeybadger | Security scanning and runtime quarantine | `Scanner`, `Quarantine`, `Scan` | - |
| reminder | Recurring family reminders tied to family-state dates | `ReminderStore`, `Scheduler` | agentcore, config, gateway, store |
| skillbridge | Skill loading and registration | `Registry`, `List`, `Install` | - |
| skilladapt | Multi-format skill adapters (SKILL.md / SOUL.md) | `Skill` | - |
| toolreg | Unified tool registry and token-budget selection | `Tool`, `Registry`, `SelectOptions` | mcp |
| mcp | Multi-transport tool server pool (stdio/HTTP/SSE) | `Pool`, `NewPool`, `RegisterFromConfig` | - |
| llm | LLM client abstraction and tool calling | `Client`, `NewClient`, `Ping` | - |
| classifier | Message classification and topic detection | `Classifier`, `Classify` | - |
| prompt | System prompt assembly (capabilities, rules, snapshots) | `Build`, `BuildContext` | config, familystate, usermemory |
| inference | Local LLM inference via llama-server sidecar | `Sidecar`, `NewSidecar`, `Start` | - |
| hardware | Host capability detection (RAM/CPU) for local-LLM viability | `HardwareInfo` | llm |
| todo | Todo list tool backed by DB | `Store`, `Tool` | agentcore, store |
| credstore | Machine-bound credential vault (AES-256-GCM) | `Vault`, `New`, `Decrypt` | - |
| auth | Session-based authentication and PIN management | `AuthHandler`, `HandleLogin`, `HandleSession` | store, credstore |
| middleware | HTTP middleware for session validation and auth | `WithSession`, `protect`, `conditionalProtect` | store |

### Entry points

| Binary | File | What it does |
|---|---|---|
| famclaw | cmd/famclaw/main.go | Main entry point: loads config, starts gateways, web server, and handles shutdown |
| famclaw skill | cmd/famclaw/main.go | Skill management command (install, list, remove) |

### Web / API surface

| Route | Handler file:line | Auth requirement | Purpose |
|---|---|---|---|
| / | internal/web/server.go:135 | Public | Root redirect to /setup if unconfigured, otherwise static files |
| /login | internal/web/server.go:139 | Public | Login page (PIN-based) |
| /logout | internal/web/server.go:140 | Session | Logout endpoint |
| /session | internal/web/server.go:141 | Session | Get current session |
| /api/setup/detect | internal/web/server.go:148 | Public | Detect first-boot state |
| /api/setup/pin | internal/web/server.go:149 | Public | Set first-boot PIN |
| /api/setup/unlock | internal/web/server.go:150 | Public | Unlock after machine change |
| /api/health | internal/web/server.go:151 | Public | Health check endpoint |
| /decide | internal/web/server.go:154 | HMAC token | One-click approval/deny link |
| /api/chat | internal/web/server.go:156 | Public | WebSocket chat endpoint (user from ?user= query) |
| /api/users | internal/web/server.go:159 | Session | Get list of users |
| /api/approvals | internal/web/server.go:160 | Session | Get pending/approvals |
| /api/approvals/decide | internal/web/server.go:161 | Session | Approve/deny approval request |
| /api/skills | internal/web/server.go:162 | Session | Get list of installed skills |
| /api/skills/install | internal/web/server.go:163 | Session | Install skill from URL |
| /api/skills/remove | internal/web/server.go:164 | Session | Remove installed skill |
| /api/unknown-accounts | internal/web/server.go:165 | Session | List unlinked accounts |
| /api/unknown-accounts/link | internal/web/server.go:166 | Session | Link unlinked account to user |
| /api/unknown-accounts/dismiss | internal/web/server.go:167 | Session | Mark unlinked account as dismissed |
| /api/conversations | internal/web/server.go:168 | Session | Get conversation history |
| /api/family-state/facts | internal/web/server.go:169 | Session | Get family facts |
| /api/family-state/facts/ | internal/web/server.go:170 | Session | Get specific family fact |
| /api/family-state/categories | internal/web/server.go:171 | Session | Get family categories |
| /api/family-state/categories/ | internal/web/server.go:172 | Session | Get specific category |
| /api/settings | internal/web/server.go:173 | Session | Get/modify settings |
| /api/setup/test-telegram | internal/web/server.go:174 | Conditional | Test Telegram connection (before PIN) |
| /api/setup/test-discord | internal/web/server.go:175 | Conditional | Test Discord connection (before PIN) |
| /api/stream | internal/web/server.go:176 | Session | Server-sent events for dashboard updates |

### "Where does X live?" — quick index

- Where is the policy evaluation called? → `internal/gateway/router.go:process` (line 127)
- Where is the config loaded? → `internal/config/config.go:Load` (line 345)
- Where does a Telegram message land? → `internal/gateway/telegram/bot.go` (polling) → `internal/gateway/router.go:Handle` (line 104)
- Where is the web fetch tool registered? → `cmd/famclaw/main.go` (line 588)
- Where is the approval decision notified? → `internal/gateway/router.go:createApproval` (line 252)
- Where is the database migrated? → `internal/store/db.go:migrate` (line 63)
- Where is the message gateway recorded? → `internal/store/db.go:SaveMessage` (gateway param; `MostRecentGatewayForUser` query; column backfilled to `unknown` for pre-migration rows)
- Where is the notification sent? → `internal/notify/notifier.go:Notify` (line 63)
- Where is the agent chat function defined? → `cmd/famclaw/main.go` (line 653)
- Where is the family state stored? → `internal/familystate/store.go:Store` (line 14)
- Where is the session authenticated? → `internal/web/middleware/session.go:WithSession` (line 40)
- Where is the tool cache used? → `internal/agent/agent.go:Chat` (line 289)
- Where is the tool result audited? → `internal/store/db.go:LogAudit` (line 1071)
- Where is the parent PIN stored? → `internal/credstore/vault.go` (`Vault`, AES-256-GCM); the ciphertext row lives in `vault_secrets` (`internal/store/db.go`, line 243). `cmd/famclaw/main.go` runs the vault-mismatch probe.
- Where is the LLM client created? → `cmd/famclaw/main.go` (line 442)
- Where is voice transcription done? → `internal/gateway/router.go:process` (transcribes audio attachments BEFORE the classifier and policy gates, so the transcript gets the same age/approval gating as typed text; `internal/transcribe/transcribe.go` for the OpenAI-compatible client, `internal/transcribe/reloadable.go` for the hot-reloadable wrapper). The `gateway.Transcriber` interface is defined in `internal/gateway/gateway.go`.
- Where is the gateway account linked? → `internal/gateway/router.go:handleUnknownAccount` (line 373)
- Where is the current date injected into the system prompt? → `internal/prompt/components.go:dateComponent` (clock via `BuildContext.Now`, falls back to `time.Now()`; snapshot fixtures use a fixed clock in `internal/prompt/snapshot_test.go`)
- Where is web_search's "backend unavailable" sentinel? → `internal/websearch/search.go` (`ErrUnavailable`, detected via `errors.Is`); `internal/agent/agent.go` (`webSearchError`) translates it into an honest "I could not search right now" message.
- Where is the search endpoint startup reachability check? → `cmd/famclaw/main.go` (`checkSearchEndpointReachable`)
- Where is skill security scanning? → install-time gate in `internal/skillbridge/registry.go:Install` (fails closed if HoneyBadger is unavailable); boot-load scan in `reg.ScanAll` (called from `cmd/famclaw/main.go`); runtime tool quarantine in `internal/agentcore/stage_async_scan.go` + `stage_quarantine_filter.go`. Scan results persist to `seccheck_reports` (`internal/store/db.go:SaveSecCheckReport`). HoneyBadger is fetched automatically by `make install` (`Makefile: install-scanner`) and at runtime via `honeybadger.Client.EnsureScanner`.

### Notable sharp edges

- CGO_ENABLED=0 is a hard rule — no CGO imports allowed.
- All packages must be tested with table-driven tests.
- No global state — everything is injected via constructor.
- Context is passed to all blocking calls.
- Errors must be wrapped with `fmt.Errorf("doing X: %w", err)`.
- Interfaces are used at boundaries (gateway, notifier, llm client).
- The policy engine is the gate — LLM is never called before `policy.Evaluate()` returns allow.
- Logs go to stderr, stdout is reserved for MCP JSON-RPC.
- One binary — no separate processes except MCP skill servers and the optional llama-server inference sidecar.
- The `web_fetch` tool requires an `url_allowlist` to be set to prevent SSRF attacks.
- The `tools.web_fetch.enabled` flag is optional (default off). When enabled, `url_allowlist` is required — an empty list denies all fetches (SSRF guard).
- The `tools.browser` tool reuses the `tools.web_fetch.url_allowlist` as its host gate.
- The `tools.web_search` tool has its own `enabled` flag, but it requires `tools.web_fetch.enabled=true` (it reuses web_fetch's `url_allowlist` as its host gate and won't start otherwise).
- **Background work spawned from a builtin handler must NOT derive its context from the inbound chat-turn `ctx`, but MUST be rooted at the server-lifetime context** so it outlives the chat turn yet is cancelled on shutdown. The inbound context is the chat-turn context from `session.go:handleRequest`, which is cancelled via `defer cancel()` when the handler returns (~1s after the ack is sent). The research subagent (`spawn_agent`) derives its timeout context from `Agent.lifetimeCtx` (threaded from `gwCtx` in `cmd/famclaw/main.go`), which is cancelled by `gwCancel()` during graceful shutdown. Using `context.Background()` instead would sever the subagent from shutdown entirely — on SIGTERM the process exits while the subagent runs to its 300s timeout, losing the result silently; using the chat-turn `ctx` kills the subagent immediately. The `resolveSubagentOutcome` helper in `internal/agent/research.go` centralizes the result-vs-done classification so `context.Canceled` (shutdown) is never re-labelled as `ResearchStatusTimedOut`.
- The `approvalID` function uses `sha256` hashing for uniqueness.
- The `notify.GenerateToken` function creates time-limited HMAC tokens.
- The `toolcache` cache tool is only attached when it already exists (auto-created when `cfg.Tools.ToolCache.Enabled` or `cfg.Tools.WebFetch.Enabled` is true).
- The `subagent` scheduler has a concurrency cap of 2.
- Proactive delivery to a family member resolves its destination from `gateway_accounts` (the authoritative reach record), but being linked is not the same as being initable: Discord is always initable (a bot can open a DM to a shared-guild member), while Telegram is initable only after the user has sent the bot at least one message (Telegram forbids bots from starting conversations). `sendmsg.ResolveDestination` picks the most-recently-used initable gateway and returns an actionable error when nothing can be initiated. `store.DB.MostRecentGatewayAndExternalIDForUser` is still used by the reminder tool for the delivery target. A linked-but-silent Telegram user is NOT proactively reachable until they message the bot; a linked-but-silent Discord user is.
- The reminder scheduler retries a failed proactive delivery up to `reminder.MaxDeliveryAttempts` (3); after that it marks the reminder dispatched to stop the retry loop against a permanently-failing destination (e.g. a Discord 404). The counter persists in `reminders.delivery_attempts`.
- The `inference` sidecar only starts if `cfg.Inference.Backend == "llama-server"`.
- The `mcp` pool is always constructed; server registration (`RegisterFromConfig`) only runs when `cfg.Skills.MCPServers` is non-empty.
- The `web` server has a `vaultMismatch` flag that triggers the unlock page.
- The `auth` system uses a `session` cookie for admin access.
- The `web` server has a `conditionalProtect` function for setup endpoints.
- The `web` server has a `hasPINConfigured` function for setup detection.
- The `web` server has a `getParentPINCiphertext` function for auth.
- The `web` server has a `resolveParentUserID` function for admin access.
- The `web` server has a `broadcastDashboardUpdate` function for live updates.
- The `web` server has a `handleStream` function for SSE updates.
- The `web` server has a `handleConversations` function for message history.
- The `web` server has a `handleUsers` function for user list.
- The `web` server has a `handleApprovals` function for approval list.
- The `web` server has a `handleSkills` function for skill list.
- The `web` server has a `handleSettings` function for settings.
- The `web` server has a `handleTestTelegram` and `handleTestDiscord` functions for testing.
- The `web` server has a `handleDecideLink` function for one-click approval links.
- The `web` server has a `handleHealth` function for health checks.
- The `web` server has a `handleRoot` function for root redirection.
- The `web` server has a `handleSetupDetect`, `handleSetupPIN`, and `handleSetupUnlock` functions for setup.
- `file_*` tools are confined per-conversation (`conversations/<gateway>/<identity>`), not per-user. The `conversationSandboxRoot` function in `internal/agent/agent.go` sanitizes gateway/external-ID/group-ID with a strict `[A-Za-z0-9._-]` allowlist and asserts containment. Family facts and user memories are global — partitioning only affects `file_*` tools. Legacy per-user/per-group directories are migrated to `conversations/_legacy/` by `MigrateSandbox` at startup (`cmd/famclaw/main.go`).

---

### What this is
Secure family AI assistant in Go.  
Runs locally on RPi/Mac; Telegram/Discord + web.  
Every message passes policy engine before LLM.  
Single CGO_ENABLED=0 binary.

### Skills repo
Skills live in `famclaw/skills` — never create a skills/ dir here.

First-party skills (like `family-knowledge`) are maintained in `skills-repo/family-knowledge/` and automatically synced to `famclaw/skills/family-knowledge/` during the build process.

### Module path
`github.com/famclaw/famclaw`

---

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

---

## Policy engine rules — NEVER violate
- `policy.Evaluate()` is called on EVERY message from EVERY gateway  
- A "parent" role always returns allow — but identity must be verified first  
- Unknown gateway accounts always get the onboarding response — never reach the LLM  
- Hard-blocked categories CANNOT be overridden by approvals

---

## Code review focus / recurring pitfalls

famclaw code must be correct, efficient, tested, and idiomatic Go. The bug classes below recur in famclaw PRs (from CodeRabbit + no-mistakes review history) — avoid introducing them; reviewers should watch for them.

**Correctness**
- Brittle string comparisons for logic decisions (e.g. `turn.Output != raw`) — use structured/normalized comparison, not raw string equality. (PR #182)
- Thread request `context` through all async paths for cancellation/timeouts — do not drop it in session handling or LLM calls. (PR #171)
- Gate features on both the flag and the underlying system being enabled (e.g. `cfg.SecCheck.Enabled` AND the notify flag). (PR #167)

**Efficiency & idiomatic Go**
- Never `defer` inside a loop — defers accumulate until the function returns and can exhaust resources (file handles, connections, contexts); do cleanup explicitly each iteration or extract the loop body into its own function.
- Preallocate slices/maps with known capacity; avoid repeated allocations and unnecessary copies in hot paths.
- Extract shared HTTP request flows (context timeout, error handling, body close) into helpers to eliminate duplication. (PR #172)
- Idiomatic Go: early returns over deep nesting, wrap errors with `%w` and match via `errors.Is/As`, keep `gofmt`/`go vet`/`staticcheck` clean, no unused code.

**Stability**
- Close/`Shutdown()` all resources on graceful exit — session pools, notification channels — to avoid goroutine/resource leaks. (PR #171)
- Never swallow errors from notifier calls (`r.notifier.Notify`) — log them, or parents miss approvals. (PR #170)
- On queue-full, notify the caller (immediate timeout/error) instead of silently dropping and blocking goroutines. (PR #171)

**Security**
- Buffer all streamed LLM tokens before the OPA output gate evaluates — no token reaches the gateway until the full response is gated. (PR #182)
- Evaluate each policy stage (input/tool/output) exactly once at the right point — do not skip or double-evaluate. (PR #182)

**Tests**
- Add `expectTokens` (or equivalent) assertions on allowed paths, not just blocked ones. (PR #182)
- Use table-driven tests (project convention). (PR #167, #172)
- Use the configurable `--config` path; never hardcode `config.yaml`. (PR #169)

---

## Test commands
- `go test ./...`  
- `opa test internal/policy/policies/family/ internal/policy/policies/data/ -v`  
- Integration: `go test ./... -tags integration`

## Build
- `make cross` (all targets, CGO_ENABLED=0)

## Crew workflow discipline
- Commit early and often; don't sit on uncommitted work.
- One concern per PR — keep changes focused and reviewable.
- Resolve EVERY code-review comment: either fix it, or reply on the PR with a clear rationale (out-of-scope / false-positive / accepted-tradeoff). Never leave a review comment silently unaddressed.
- Verify before declaring something impossible — reproduce/test the claim.
- No junk commits (no "wip"/"fix" noise in the final history; squash/clean up).
- Quality bar: correct, efficient, well-tested, idiomatic Go (no `defer` in loops, preallocate, wrap errors with `%w`, `gofmt`/`vet`/`staticcheck` clean, early returns, no duplication). Efficiency and idiom are first-class review criteria, not just correctness.

## README rule
After a change lands, update README status + structure; only what exists.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
