# PromptBuilder Finish — Plan

## Audit summary

`feat/promptbuilder-skeleton-v3` is at the same SHA as `origin/main` (no
commits ahead). PR #114 (12-component skeleton) and PR #117 (snapshot +
behavioral tests) already landed, so the bulk of the spec's work is on
main. The dispatch worktree is also at `origin/main`.

12 components present in `internal/prompt/`:

| Slot           | Function                | File          |
| -------------- | ----------------------- | ------------- |
| oauth_prefix   | oauthPrefixComponent    | components.go |
| identity       | identityComponent       | builder.go    |
| user           | userComponent           | components.go |
| family         | familyComponent         | components.go |
| age            | ageComponent            | components.go |
| capabilities   | capabilitiesComponent   | components.go |
| skills         | skillsComponent         | components.go |
| policy         | policyComponent         | components.go |
| approvals      | approvalsComponent      | components.go |
| gateway        | gatewayComponent        | components.go |
| output         | outputComponent         | components.go |
| memory         | memoryComponent         | components.go |

All slots filled — gap on components is zero.

`internal/agent/agent.go:493` already calls `prompt.Build` — wiring is in.

## Real gaps vs SPEC

1. **Token budget tests are looser than acceptance gate 5.**
   Existing limits: parent 1100, child 750.
   Acceptance gate 5 demands: parent ≤900, child ≤650.
   Current actual usage (from `-v` test output): parent ~403, child ~418.
   Budgets must be tightened to enforce the gate.
2. **Snapshot files use `child_` prefix; spec demands the bare age-group
   names.** Currently:
   - `testdata/parent.snap` (matches)
   - `testdata/child_age_13_17.snap` → spec wants `age_13_17.snap`
   - `testdata/child_age_8_12.snap`  → spec wants `age_8_12.snap`
   - `testdata/child_under_8.snap`   → spec wants `under_8.snap`
   `snapshot_test.go` and `behavioral_test.go` reference the existing
   paths and must be updated when the files are renamed.

## Phases

### Phase 1 — tighten token budgets in `builder_test.go`
- Parent budget: 1100 → 900
- Child budget: 750 → 650
- Parent-with-builtin-tools budget: 1100 → 900 (consistency)
- Verify all three still pass.

### Phase 2 — rename snapshot files to spec layout
- `git mv` the three child snapshots to drop the `child_` prefix.
- Update `snapshot_test.go` `file:` paths to match.
- Behavioral test (`behavioral_test.go`) only references persona keys,
  not snapshot file paths — no change needed there.
- Re-run `go test ./internal/prompt/... -count=1` — snapshot diff must be
  byte-identical to the renamed files (no content change).

### Phase 3 — gates
- `CGO_ENABLED=0 go build ./...`
- `CGO_ENABLED=0 go vet ./...`
- `CGO_ENABLED=0 go test ./internal/prompt/... -count=1`
- `CGO_ENABLED=0 go test ./... -count=1`
- Confirm `git diff --stat HEAD` is non-empty.
