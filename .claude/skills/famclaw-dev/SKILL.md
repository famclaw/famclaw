---
name: famclaw-dev
description: FamClaw test gates, coverage targets, and cross-compile/release steps. Use when running tests, checking coverage, or building/releasing famclaw.
---

# FamClaw dev gates

## Tests
- Unit: `go test ./internal/PACKAGE/...`
- Policy (OPA): `opa test internal/policy/policies/family/ internal/policy/policies/data/ -v`
- Integration: `go test -tags integration ./...`
- Coverage target: >80% on policy, identity, classifier, gateway/router.
- CI blocks merge if tests fail or the binary doesn't cross-compile to arm64.

## Cross-compile (all `CGO_ENABLED=0`)
`make build` · `make cross-rpi3` (linux/arm/v7) · `make cross-rpi4` (linux/arm64, also rpi5) · `make cross-android` · `make cross-mac-arm` · `make cross-mac-intel` · `make cross-linux64` · `make cross` (all).

## Definition of done
`go build ./...` clean · `go vet ./...` clean · `go test ./...` pass · `opa test` pass · `make cross` all targets · integration proves a blocked child message never reaches the LLM.
