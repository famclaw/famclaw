# Contributing to FamClaw

Thanks for your interest in making AI safer for families.

**Design principle:** If a non-technical parent can't figure it out in 30 seconds, it's a bug.

---

## Getting started

```bash
git clone https://github.com/famclaw/famclaw
cd famclaw
make build
./bin/famclaw --config config.yaml
# Open http://localhost:8080 — you'll see the setup wizard
```

### Run tests

```bash
# Unit tests (all packages)
go test ./...

# OPA policy tests
opa test internal/policy/policies/family/ internal/policy/policies/data/ -v

# Integration tests
go test -tags integration ./... -v

# E2E tests (starts a real server)
go test -tags e2e ./e2e/... -v

# Cross-compile check (must pass — we ship to RPi, Mac, Android)
make cross
```

---

## Coding rules

These are enforced across the project (see [CLAUDE.md](./CLAUDE.md) for the full list):

1. **`CGO_ENABLED=0` always** — every package must cross-compile cleanly
2. **No CGO imports** — use `modernc.org/sqlite` not `mattn/go-sqlite3`
3. **Table-driven tests** — every package has `_test.go` with table-driven cases
4. **No global state** — everything injected via constructor
5. **Context everywhere** — all blocking calls take `context.Context` as first arg
6. **Errors wrapped** — `fmt.Errorf("doing X: %w", err)` not bare returns
7. **Interfaces at boundaries** — gateway, notifier, LLM client are interfaces
8. **Policy is the gate** — LLM is NEVER called before `policy.Evaluate()` returns allow
9. **Logs to stderr** — stdout is reserved for MCP JSON-RPC in skill servers
10. **One binary** — no separate processes except MCP skill servers (spawned on demand)

---

## Where to contribute

- **WhatsApp gateway** — currently a placeholder, needs whatsmeow QR pairing flow
- **i18n** — translate topic taxonomy (`internal/policy/policies/data/topics.json`) and web UI
- **Docker** — Dockerfile and docker-compose improvements, multi-arch builds
- **Topic taxonomy** — expand `internal/policy/policies/data/topics.json` with more categories
- **Home Assistant integration** — sensor for approval queue, automations
- **Voice interface** — wake word + speech-to-text for hands-free family use
- **Hardware guides** — test on specific boards (RPi Zero 2W, LicheeRV, etc.), document results
- **Web UI** — child-friendly chat view, parent dashboard polish, mobile responsiveness

---

## Pull requests

1. Fork the repo
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Add tests — every package has `_test.go`
4. Run `go test ./...` and `make cross`
5. Open a PR against `main`

PRs go through the merge queue with CI checks (CodeQL, govulncheck, cross-compile, OPA tests) and CodeRabbit automated review.

---

## Reference

- [AGENTS.md](./AGENTS.md) — multi-agent build plan and package descriptions
- [docs/superpowers/](./docs/superpowers/) — design specs for major features
- [CLAUDE.md](./CLAUDE.md) — full coding rules and project structure
- [SECURITY.md](./SECURITY.md) — vulnerability reporting and release verification

## License

By contributing, you agree that your contributions will be licensed under the [AGPL-3.0](./LICENSE) license.
