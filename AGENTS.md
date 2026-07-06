## FamClaw — Agent Instructions

### What this is
Secure family AI assistant in Go.  
Runs locally on RPi/Mac; Telegram/WhatsApp/Discord + web.  
Every message passes policy engine before LLM.  
Single CGO_ENABLED=0 binary.

### Skills repo
Skills live in `famclaw/skills` — never create a skills/ dir here.

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

## Test commands
- `go test ./...`  
- `opa test internal/policy/policies/family/ internal/policy/policies/data/ -v`  
- Integration: `go test ./... -tags integration`

## Build
- `make cross` (all targets, CGO_ENABLED=0)

## README rule
After a change lands, update README status + structure; only what exists.
