# Family State — Design Spec

**Phase 3.3** of `docs/superpowers/2026-05-12-roadmap.md`. Shared family memory: pets, dietary restrictions, important dates, allergies — plus user-defined custom categories. Safety-critical entries reach the LLM via prompt injection; the rest are tool-fetched on demand. Precedes Phase 3.1 (cron/reminders), which uses the `important_dates` rows as its source data (see Section 10 for the hook spec).

Author: brainstormed 2026-05-13.

## 1. Motivation

The bot today knows nothing about the family beyond identity (name, role, age_group from `config.yaml`). When asked "what's for dinner?", it cannot factor in Teo's peanut allergy or Julia's vegetarianism. When asked "remind me about the dentist Thursday", there's no place to put that fact. Conversation-level memory works inside one session and dies; persistent shared family knowledge is missing.

Phase 3.3 introduces a small structured store of family facts that:

- **Always reaches the model on safety-critical categories** (allergies, dietary restrictions) — via prompt injection in `memoryComponent`, the already-wired placeholder at `internal/prompt/components.go:211`.
- **Is fetched on demand for other categories** (pets, important_dates, custom) — via a `get_family_state` tool the LLM calls when relevant.
- **Is extensible** — parents can create new categories beyond the four ship-with set, using the same uniform row shape.
- **Reuses existing infrastructure**: the `approvals` table for kid-proposed facts, `audit_log` for write history, the parent-session web dashboard for editing, the OPA `admin_tools` set for parent-only mutations.

## 2. Scope (v1)

In:

- Two new SQLite tables (`family_fact_categories`, `family_facts`) plus seed rows for the four built-in categories
- One new package `internal/familystate/` with CRUD + snapshot rendering
- Five new builtin tools (`get_family_state`, `set_family_fact`, `delete_family_fact`, `add_family_category`, `propose_family_fact`)
- One new OPA admin tool set entry per parent-only tool
- One new web dashboard page for parents to edit facts and manage categories
- Flip `memoryComponent` from inert to producing the safety-fact block
- Kid-proposal → parent-approval flow piggybacking on the existing `approvals` table with a new payload kind
- Tests: unit (familystate), snapshot (prompt), handler (agent), OPA (rego), web handler, one integration scenario

Out (v2 or never):

- Stress test for concurrent UPSERTs
- Property-based fuzz on length caps (simple validation is sufficient)
- Cross-family / multi-tenant story (Famclaw is single-tenant)
- Migration of identity fields (date-of-birth, etc.) from `config.yaml` into `important_dates` — no such fields exist in config today
- Tier-1 LLM summarization of family_state when it grows large — deferred until volume justifies it
- Privacy gradient (younger kid can't read older sibling's facts) — Section 1 decision: fully open read within family

## 3. Architecture

### 3.1 Data model

```sql
CREATE TABLE family_fact_categories (
    name          TEXT PRIMARY KEY,
    description   TEXT NOT NULL,
    always_inject INTEGER NOT NULL DEFAULT 0,   -- 0 | 1
    is_builtin    INTEGER NOT NULL DEFAULT 0,   -- 0 | 1, builtin cannot be deleted
    created_at    INTEGER NOT NULL,             -- unix seconds (matches Phase 2 toolcache style)
    updated_at    INTEGER NOT NULL
);

CREATE TABLE family_facts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    category   TEXT NOT NULL REFERENCES family_fact_categories(name) ON DELETE RESTRICT,
    subject    TEXT NOT NULL,        -- username from config.Users OR the literal 'family'
    label      TEXT NOT NULL,        -- e.g. 'peanuts', 'Stella', 'Saturday'
    value      TEXT NOT NULL,        -- e.g. 'severe — EpiPen in Mom's purse'
    created_by TEXT NOT NULL,        -- user who created this row
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(category, subject, label)
);
CREATE INDEX idx_family_facts_subject  ON family_facts(subject);
CREATE INDEX idx_family_facts_category ON family_facts(category);
```

Migration runs as an additive step in `internal/store/db.go:migrate()`, mirroring the established pattern (Phase 2 `tool_result_cache` is the most recent precedent).

Seeded rows (idempotent `INSERT … ON CONFLICT DO NOTHING`):

| name | description | always_inject | is_builtin |
|---|---|---|---|
| `allergies` | Per-person allergies and severity. Always visible to the assistant for safety. | 1 | 1 |
| `dietary_restrictions` | Per-person or family dietary patterns (vegetarian, kosher, halal, gluten-free, etc.). Always visible to the assistant. | 1 | 1 |
| `important_dates` | Birthdays, anniversaries, recurring family events. Read on demand. Phase 3.1 reminders read this table. | 0 | 1 |
| `pets` | Family pets — names, species, notes. Read on demand. | 0 | 1 |

Length caps (handler-side validation; not a CHECK constraint to keep migration simple):
- `category.name` ≤ 32 chars, regex `[a-z0-9_]+`
- `category.description` ≤ 256 chars
- `fact.label` ≤ 64 chars
- `fact.value` ≤ 512 chars
- `fact.subject` must match a known `config.Users[].Name` or equal `family`

### 3.2 Package layout

```
internal/familystate/
    store.go           // Store wrapping *store.DB; CRUD on categories + facts
    store_test.go
    snapshot.go        // Snapshot + IsEmpty() + Render() for prompt injection
    snapshot_test.go
    proposal.go        // helpers to encode/decode proposal payloads in approvals.query_text
    proposal_test.go
    errors.go          // ErrBuiltinCategory, ErrUnknownCategory, ErrUnknownSubject, ErrCategoryNotEmpty
```

The package depends on `internal/store` and `internal/config` only. No agent or LLM imports — this is a data package.

### 3.3 Prompt integration

`internal/prompt/builder.go` extends `BuildContext`:

```go
type BuildContext struct {
    Cfg          *config.Config
    User         *config.UserConfig
    Gateway      string
    Skills       []string
    HardBlocked  []string
    BuiltinTools []string
    FamilyState  *familystate.Snapshot  // NEW; may be nil if load failed
}
```

`memoryComponent` in `internal/prompt/components.go` flips from `("",false)` to:

```go
func memoryComponent(c BuildContext) (string, bool) {
    if c.FamilyState == nil || c.FamilyState.IsEmpty() {
        return "", false
    }
    return c.FamilyState.Render(), true
}
```

`Snapshot.Render()` produces (example):

```
Family safety facts:
- Allergies: Teo — peanuts (severe, EpiPen in Mom's purse).
- Dietary restrictions: Family — kosher. Julia — vegetarian.
```

Deterministic ordering: categories alphabetized, then subjects alphabetized within each (`family` sorts first within its category for readability), then labels alphabetized within each subject. Snapshot tests pin this format exactly.

### 3.4 Agent integration

`internal/agent/agent.go`:

- New field on the Agent struct: `familyState *familystate.Store`
- Before each `prompt.Build` call: `snap, err := a.familyState.AlwaysInjectedSnapshot(ctx)`. On error, log and continue with `snap = nil` (fail-open per Section 3 decision).
- Builtin tool dispatch additions:
  - `get_family_state` and `propose_family_fact` are wired into the main `internal/agent/agent.go` dispatch switch alongside `web_fetch`, `spawn_agent`, `tool_result_more` (the all-roles tools).
  - `set_family_fact`, `delete_family_fact`, `add_family_category`, `delete_family_category` follow the existing admin-tool pattern: one file per tool under `internal/agent/tools/admin/` (mirrors `internal/agent/tools/admin/approve_request.go` etc.).
- Subagents receive `get_family_state` in `ExecutorDeps.BuiltinDefs` (mutation tools excluded — subagents are read-only by design).

### 3.5 OPA tool policy

`internal/policy/policies/family/tool_policy.rego` — extend the existing `admin_tools` set:

```rego
admin_tools := {
    "list_pending_approvals",
    "approve_request",
    "deny_request",
    "list_users",
    "set_user_role",
    "list_unknown_accounts",
    "link_account",
    # new in Phase 3.3:
    "set_family_fact",
    "delete_family_fact",
    "add_family_category",
    "delete_family_category",
}
```

`get_family_state` and `propose_family_fact` are NOT in `admin_tools` — all roles can call them. No new explicit allow rule is needed; the default allow-non-admin-for-children path covers them.

OPA test additions in `tool_policy_test.rego`: one test per new tool × {parent, child age_13_17, child age_8_12, child under_8}.

## 4. Tool surface (LLM-facing)

| Tool | Args | Returns | Role gate | Side effects |
|---|---|---|---|---|
| `get_family_state` | `category?: string` — optional filter | Rendered text grouped by category; if filtered, just that one | all | none |
| `set_family_fact` | `category: string, subject: string, label: string, value: string` | `"ok"` or error message | parent (admin_tools) | UPSERT row; audit_log entry |
| `delete_family_fact` | `id: int` | `"ok"` or error | parent | DELETE row; audit_log entry |
| `add_family_category` | `name: string, description: string, always_inject?: bool` | `"ok"` or error | parent | INSERT category; audit_log entry |
| `delete_family_category` | `name: string` | `"ok"` or error | parent | DELETE category (only if empty AND not builtin); audit_log entry |
| `propose_family_fact` | `category: string, subject: string, label: string, value: string, reason?: string` | `"Proposal sent to parents."` or error | all | If caller is parent: auto-apply (same as set_family_fact). Otherwise: INSERT into `approvals` with payload kind=`family_fact_proposal`; existing notify path fires. |

Tool descriptions in `internal/agent/tooldef.go` follow the Phase 2 lesson (per `[[reference_nemotron_toolcall_format]]` and Phase 2's spawn_agent fix): include explicit WHEN-TO-USE and WHEN-NOT-TO-USE bullets plus one concrete example call. Small local models need this concreteness or the affordance is invisible to them.

## 5. Web dashboard

New page at `/dashboard/family-state` (parent-session middleware required):

- Top: list of categories with description + always_inject badge + count of facts. "Add category" button opens a small form (name, description, always_inject checkbox).
- Per category: expandable list of facts. Each row shows subject, label, value, last-updated. Inline edit + delete with confirmation modal.
- Built-in categories show a lock icon in the delete column (delete disabled).
- Audit-log link in the page header points at the existing `/dashboard/audit` filtered to `tool_name LIKE 'family_%'`.

Handler files:

- `internal/web/familystate_handler.go` — JSON endpoints under `/api/family-state/`
- `internal/web/static/family-state.html` — template, follows the existing dashboard styling

No new JS framework. Hand-rolled fetch + simple DOM updates, matching `unknown-accounts.html` and `audit.html` patterns.

## 6. Kid proposal flow

Re-uses the existing `approvals` table — no new table.

`approvals.query_text` stores a JSON envelope:

```json
{
  "kind": "family_fact_proposal",
  "category": "user_preferences",
  "subject": "teo",
  "label": "favorite_pizza",
  "value": "pepperoni",
  "reason": "Teo said so in chat",
  "proposed_by": "teo"
}
```

`approvals.category` column stores the literal string `family_fact_proposal` (distinct from the LLM topic-classification categories) so list/filter queries don't conflate them. Existing `notify` package fires; existing parent approve/deny UI works without changes.

When a parent approves, the existing approval handler in `internal/agent/tools/admin/approve_request.go` reads the JSON envelope, dispatches on `kind`, and calls `familystate.Store.UpsertFact` with `created_by` set to `proposed_by` and an audit entry tagged `approved_by`. The dispatch-on-kind logic is the only behavior change needed in `approve_request.go` — adding a `family_fact_proposal` case alongside whatever exists today.

## 7. Data flow examples

### Read — safety category (always-injected)

1. Discord: "what should we make for dinner?"
2. `agent.Chat` builds the BuildContext:
   ```go
   snap, _ := a.familyState.AlwaysInjectedSnapshot(ctx)  // ignore err, fail open
   prompt.Build(prompt.BuildContext{..., FamilyState: snap})
   ```
3. `memoryComponent` produces the safety block.
4. LLM sees allergies + dietary restrictions in the system prompt and answers accordingly.

### Read — non-safety category (on-demand)

1. Discord: "what's our cat's name?"
2. LLM emits `get_family_state(category="pets")`.
3. `tool_policy.rego` → allow.
4. `handleGetFamilyState` → `familystate.Store.ListFacts(ctx, FilterOpts{Category: "pets"})`.
5. Tool result returned to LLM, response emitted.

### Write — parent (web)

1. Parent at `/dashboard/family-state` clicks "Add fact" on `pets`.
2. POST `/api/family-state/facts` with body `{category:"pets", subject:"family", label:"Stella", value:"cat, age 5"}`.
3. Handler validates parent session, validates payload, calls `Store.UpsertFact`.
4. `DB.LogAudit` writes audit row with `tool_name=family_state_web_upsert`.
5. JSON 200 response; dashboard refreshes the list.

### Write — parent (chat)

1. Telegram parent: "save that Stella the cat eats salmon"
2. LLM emits `set_family_fact(category="pets", subject="family", label="Stella", value="cat — eats salmon")`.
3. `tool_policy.rego` → allow (admin_tools).
4. Handler validates payload, calls `Store.UpsertFact`, writes audit row with `tool_name=set_family_fact`.
5. Tool result `"ok"` returns to LLM.

### Write — kid proposal

1. Discord (Teo, age_13_17): "remember I love pepperoni pizza"
2. LLM emits `propose_family_fact(category="user_preferences", subject="teo", label="favorite_food", value="pepperoni pizza")`.
3. Handler checks the category exists. If not, returns `"Category 'user_preferences' doesn't exist. A parent needs to create it first."`.
4. If the category exists, handler creates an `approvals` row, fires existing parent notify path.
5. Parent /approve in Telegram → existing approval handler reads payload kind → `familystate.Store.UpsertFact`.
6. Audit log entry.

## 8. Error handling

| Case | Behavior |
|---|---|
| `set_family_fact` with unknown category | Tool returns `"unknown category 'X' — a parent can create it via add_family_category"` |
| `set_family_fact` with subject not in config.Users + != 'family' | Tool returns `"subject 'X' is not a family member"` |
| `delete_family_category` for `is_builtin=1` | `ErrBuiltinCategory`; tool returns `"can't delete a built-in category"` |
| `delete_family_category` when facts exist | FK RESTRICT → `ErrCategoryNotEmpty`; tool returns `"category has N facts; delete them first"` |
| `add_family_category` for name that already exists | Idempotent — updates description + always_inject if caller is parent |
| `propose_family_fact` from a parent | Auto-applies (no approval round-trip), audit row tagged `auto_apply_parent=true` |
| `AlwaysInjectedSnapshot` with empty tables | `Snapshot.IsEmpty()` returns true; `memoryComponent` returns `("", false)` |
| Snapshot DB read fails | Log via `slog`; `FamilyState=nil` passed to BuildContext; `memoryComponent` returns `("", false)`; prompt still builds. Conservative fail-open. |
| Concurrent writes from two parents | `UNIQUE(category, subject, label)` constraint + UPSERT semantics; last write wins; `updated_at` reflects latest |
| Over-length input | Handler-side validation returns `"label too long (max 64 chars)"` etc. before DB call |
| Subagent invokes a mutation tool | Tool not in `ExecutorDeps.BuiltinDefs` for subagents; LLM gets unknown-tool error |

## 9. Testing

### `internal/familystate/` (unit)

| File | Tests |
|---|---|
| `store_test.go` | Table-driven: insert, upsert, delete, list with filters; FK RESTRICT on category-with-facts; builtin-category delete refused; migration idempotency (run twice, seed rows count = 4) |
| `snapshot_test.go` | `AlwaysInjectedSnapshot` reads only `always_inject=1` rows; empty → `IsEmpty()`; deterministic ordering across multiple subjects |
| `proposal_test.go` | JSON envelope round-trip via `EncodeProposal` / `DecodeProposal` |

### `internal/prompt/` (snapshot)

Extend the existing golden-file test infrastructure under `internal/prompt/testdata/`:

| Snapshot file | What it pins |
|---|---|
| `parent.snap` (existing) | Extended: no FamilyState in BuildContext → no memory section |
| `age_13_17_with_safety.snap` (new) | FamilyState with allergies+dietary → exact memory section format |
| `age_8_12.snap` (existing) | No FamilyState → no memory section |
| `under_8_with_dietary.snap` (new) | FamilyState with only dietary → memory section present, dietary only |

### `internal/agent/` (handler-against-OPA)

Add to `stages_test.go` following the `bare_builtin_name_normalized` pattern:

- `TestSetFamilyFact_ChildBlockedByPolicy` — child user, tool policy denies, tool result is the OPA reason
- `TestSetFamilyFact_ParentUnknownCategory` — parent, but category doesn't exist → tool message, no DB write
- `TestProposeFamilyFact_CreatesApprovalRow` — child proposes; verify approvals table row + payload kind
- `TestGetFamilyState_RendersFilteredCategory` — pet category exists, LLM filters, exact rendered output

### `internal/policy/policies/family/tool_policy_test.rego` (OPA)

Per new tool × {parent, age_13_17, age_8_12, under_8} → expected allow/deny. Six new tests total.

### `internal/web/` (handler)

- `TestFamilyStatePostAsParent_200_AndDBRow`
- `TestFamilyStatePostNoSession_401`
- `TestFamilyStatePostAsChild_403` (if a child-session UI path exists in v1 — currently doesn't, so this is omitted)
- `TestDeleteBuiltinCategory_400`
- `TestDeleteCategoryWithFacts_400`

### Integration test (`integration_test.go`, build tag `integration`)

One scenario: parent on mock-Discord → router → policy → agent → `set_family_fact` tool call → DB row written → next agent turn loads `AlwaysInjectedSnapshot` → memoryComponent emits the new fact in the prompt sent to the mock LLM server. Validates the loop without mocking the package boundary.

### Coverage target

≥80% on `internal/familystate/` per the project rule. Existing CI gates apply: govulncheck (blocks), gosec (warns), race detector on agent+router (already covers the new handler).

## 10. Phase 3.1 hook

`important_dates` is the shared substrate. Schema fits both v1 read-on-demand AND future Phase 3.1 cron:

- Phase 3.1 will add a `reminders` table referencing `family_facts.id` for date-bound rows
- `value` field for an `important_dates` row contains an ISO date (`YYYY-MM-DD`) + optional time; recurrence noted in the `label` or as a `recurrence` extension column (future)
- v1 stores the data; Phase 3.1 wires the cron and the delivery

Explicit non-decision: v1 does NOT add a `recurrence` column. Phase 3.1 will decide whether to (a) parse it out of the `value` text, (b) add a column then, or (c) introduce a separate `recurring_dates` category. Whatever Phase 3.1 picks, v1's `important_dates` rows remain valid.

## 11. Open questions / explicit non-goals

- **Per-user privacy gradient**: explicitly out of v1 per Section 1 user choice. Anyone in the family can read any fact via `get_family_state`. If a real privacy concern emerges, a `visibility TEXT` column on `family_facts` is the natural extension point.
- **Multilingual `label`/`value`**: not specially handled; stored as UTF-8 like all other text fields.
- **Backup/export**: not a v1 feature. Standard `~/.famclaw/famclaw.db` backup covers it; specific JSON export is v2 if asked.
- **Bulk import from existing `config.yaml`**: nothing in config to import. If `config.UserConfig` ever gains a `birth_date` field, a one-shot migration into `important_dates` is a v1.1 add.

## 12. Acceptance criteria

- `go test ./internal/familystate/... ./internal/prompt/... ./internal/agent/... ./internal/web/...` passes
- `opa test internal/policy/policies/family/ internal/policy/policies/data/` passes
- `make cross` builds all 6 targets cleanly (`CGO_ENABLED=0`)
- Integration scenario passes
- Manual smoke on a fresh DB: open dashboard, add a `pets` row, ask Discord bot "what's our cat's name", get correct answer
- Manual smoke on a populated DB with `allergies`: ask Discord "what should we make for dinner", model reply references the allergies it saw in the prompt
- Tools appear in the agent's tool list with descriptions concrete enough that Nemotron-30B calls them when relevant (per the Phase 2 / spawn_agent lesson)

## 13. Implementation plan

To be written next via the `superpowers:writing-plans` skill. Expected shape (subject to that skill's process):

1. Wave 1 (parallel): `internal/familystate/` package + tests; OPA tool_policy additions + rego tests
2. Wave 2 (sequential): `internal/store/db.go` migration; wire `familystate.Store` into agent + prompt
3. Wave 3 (parallel): web handler + dashboard page; tool handlers in `internal/agent/agent.go`; subagent BuiltinDefs update
4. Wave 4: integration test; manual smoke; release notes
