# Family State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Phase 3.3 family state — shared family memory (allergies, dietary restrictions, important_dates, pets, plus parent-extensible custom categories), with safety-critical entries injected into every system prompt and the rest read on-demand via LLM tools.

**Architecture:** New `internal/familystate/` package (pure data) wrapping two new SQLite tables. Snapshot for prompt-injection feeds the existing `memoryComponent` placeholder at `internal/prompt/components.go:211`. Five new builtin tools (one read tool dispatched in `agent.go`, four mutation tools in `internal/agent/tools/admin/`). OPA `tool_policy.rego` gates mutations (plus a synthetic `family_fact_proposal_auto_apply` rule). Kid-proposal flow piggybacks on the existing `approvals` table via a JSON envelope dispatched in `approve_request.go`. Web dashboard adds one new page.

**Tech Stack:** Go 1.22, `modernc.org/sqlite` (no CGO), OPA v0.68, `log/slog`. All cross-compile constraints from `CLAUDE.md` apply (`CGO_ENABLED=0`).

**Spec:** `docs/superpowers/specs/2026-05-13-family-state-design.md` (commit `03ed188`). All 6 council-locked fixes are folded into this plan.

---

## File map (created / modified)

**Create:**

- `internal/familystate/errors.go` — sentinel errors
- `internal/familystate/store.go` — CRUD methods on `*store.DB`
- `internal/familystate/store_test.go`
- `internal/familystate/snapshot.go` — Snapshot, IsEmpty, Render, UnavailableSnapshot
- `internal/familystate/snapshot_test.go`
- `internal/familystate/proposal.go` — JSON envelope encode/decode
- `internal/familystate/proposal_test.go`
- `internal/agent/tools/admin/set_family_fact.go`
- `internal/agent/tools/admin/delete_family_fact.go`
- `internal/agent/tools/admin/add_family_category.go`
- `internal/agent/tools/admin/delete_family_category.go`
- `internal/web/familystate_handler.go`
- `internal/web/familystate_handler_test.go`
- `internal/web/static/family-state.html`
- `internal/prompt/testdata/age_13_17_with_safety.snap`
- `internal/prompt/testdata/under_8_with_dietary.snap`

**Modify:**

- `internal/store/db.go` — extend `migrate()` with two tables + seed
- `internal/policy/policies/family/tool_policy.rego` — add four admin tools + auto-apply rule
- `internal/policy/policies/family/tool_policy_test.rego` — add 20 new tests
- `internal/prompt/builder.go` — extend `BuildContext` with `FamilyState *familystate.Snapshot`
- `internal/prompt/components.go` — flip `memoryComponent` on
- `internal/prompt/snapshot_test.go` — new snapshot cases
- `internal/agent/agent.go` — add `familyState`, dispatch cases for `get_family_state` + `propose_family_fact`, snapshot wiring at prompt-build time, BuildContext population, subagent BuiltinDefs filter
- `internal/agent/tools/admin/tool.go` — register new mutating defs
- `internal/agent/tools/admin/approve_request.go` — dispatch-on-kind for `family_fact_proposal`
- `internal/web/server.go` — register `/api/family-state/*` routes + `/dashboard/family-state`
- `internal/web/static/index.html` — add nav link
- `integration_test.go` — 6 new scenarios

---

## Task 1 — `internal/familystate/errors.go`

**Files:**
- Create: `internal/familystate/errors.go`

- [ ] **Step 1: Create the errors file**

Write `internal/familystate/errors.go`:

```go
// Package familystate provides a structured, parent-managed store of
// per-family facts (allergies, pets, important dates, etc.) plus a
// prompt-injection snapshot for safety-critical categories.
package familystate

import "errors"

// ErrBuiltinCategory is returned when a caller tries to delete a
// category that ships with the bot (allergies, dietary_restrictions,
// important_dates, pets). These rows have is_builtin=1 in the
// family_fact_categories table.
var ErrBuiltinCategory = errors.New("family_state: cannot delete a built-in category")

// ErrUnknownCategory is returned when a fact references a category
// that does not exist in family_fact_categories.
var ErrUnknownCategory = errors.New("family_state: unknown category")

// ErrUnknownSubject is returned when a fact's subject does not match
// any name in config.Users and is not the literal 'family'.
var ErrUnknownSubject = errors.New("family_state: subject must be a configured family member or 'family'")

// ErrCategoryNotEmpty is returned when a caller tries to delete a
// category that still has at least one family_facts row referencing it.
// The FK constraint on family_facts(category) is RESTRICT.
var ErrCategoryNotEmpty = errors.New("family_state: category has facts; delete them first")

// ErrLengthCap is returned by handler-side validation when a label
// or value exceeds the documented cap (label ≤ 64, value ≤ 512,
// category.name ≤ 32, category.description ≤ 256).
var ErrLengthCap = errors.New("family_state: input exceeds length cap")
```

- [ ] **Step 2: Verify package compiles**

Run: `go build ./internal/familystate/...`
Expected: clean (no output).

- [ ] **Step 3: Commit**

```bash
git add internal/familystate/errors.go
git commit -m "feat(familystate): add sentinel errors for the package"
```

---

## Task 2 — `internal/store/db.go` migration

**Files:**
- Modify: `internal/store/db.go` (add to `migrate()` SQL block)

- [ ] **Step 1: Add the two tables to migrate()**

Locate the end of the giant `_, err := d.sql.Exec(\`...\`)` in `migrate()` (after the `tool_result_audit` block, before the closing backtick). Append:

```sql

	-- Phase 3.3 — family state (shared family memory).
	-- See docs/superpowers/specs/2026-05-13-family-state-design.md.
	CREATE TABLE IF NOT EXISTS family_fact_categories (
		name          TEXT PRIMARY KEY,
		description   TEXT NOT NULL,
		always_inject INTEGER NOT NULL DEFAULT 0,
		is_builtin    INTEGER NOT NULL DEFAULT 0,
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS family_facts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		category   TEXT NOT NULL REFERENCES family_fact_categories(name) ON DELETE RESTRICT,
		subject    TEXT NOT NULL,
		label      TEXT NOT NULL,
		value      TEXT NOT NULL,
		recurrence TEXT DEFAULT NULL,
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(category, subject, label)
	);
	CREATE INDEX IF NOT EXISTS idx_family_facts_subject  ON family_facts(subject);
	CREATE INDEX IF NOT EXISTS idx_family_facts_category ON family_facts(category);
```

- [ ] **Step 2: Add the seed for builtin categories**

After the existing decision-note `ALTER TABLE` guard near the end of `migrate()`, add:

```go
	// Phase 3.3 seed: built-in family_fact_categories. Idempotent.
	now := time.Now().Unix()
	if _, err := d.sql.ExecContext(context.Background(), `
		INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		VALUES
		  ('allergies', 'Per-person allergies and severity. Always visible to the assistant for safety.', 1, 1, ?, ?),
		  ('dietary_restrictions', 'Per-person or family dietary patterns (vegetarian, kosher, halal, gluten-free, etc.). Always visible to the assistant.', 1, 1, ?, ?),
		  ('important_dates', 'Birthdays, anniversaries, recurring family events. Read on demand. Phase 3.1 reminders read this table.', 0, 1, ?, ?),
		  ('pets', 'Family pets — names, species, notes. Read on demand.', 0, 1, ?, ?)
		ON CONFLICT(name) DO NOTHING`,
		now, now, now, now, now, now, now, now); err != nil {
		return fmt.Errorf("migrate seed family_fact_categories: %w", err)
	}
```

- [ ] **Step 3: Verify build still compiles**

Run: `go build ./internal/store/...`
Expected: clean.

- [ ] **Step 4: Verify tests still pass**

Run: `go test -count=1 ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/db.go
git commit -m "feat(store): add family_fact_categories + family_facts tables (Phase 3.3)"
```

---

## Task 3 — `internal/familystate/store.go` types + ListCategories

**Files:**
- Create: `internal/familystate/store.go`
- Create: `internal/familystate/store_test.go`

- [ ] **Step 1: Write the failing test first**

Create `internal/familystate/store_test.go`:

```go
package familystate

import (
	"context"
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)

// newTestStore opens an in-memory SQLite DB with the migration applied
// and returns a wired familystate.Store.
func newTestStore(t *testing.T) (*Store, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db), db
}

func TestListCategories_SeededBuiltins(t *testing.T) {
	s, _ := newTestStore(t)

	cats, err := s.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	wantNames := map[string]bool{
		"allergies":            true,
		"dietary_restrictions": true,
		"important_dates":      true,
		"pets":                 true,
	}
	if len(cats) != len(wantNames) {
		t.Fatalf("got %d categories, want %d", len(cats), len(wantNames))
	}
	for _, c := range cats {
		if !wantNames[c.Name] {
			t.Errorf("unexpected category %q", c.Name)
		}
		if !c.IsBuiltin {
			t.Errorf("category %q should be builtin", c.Name)
		}
	}
}
```

- [ ] **Step 2: Run the test — it should fail because Store doesn't exist**

Run: `go test ./internal/familystate/... -run TestListCategories_SeededBuiltins -v`
Expected: FAIL with "undefined: NewStore" or "undefined: Store".

- [ ] **Step 3: Write the Store type + ListCategories**

Create `internal/familystate/store.go`:

```go
package familystate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// Store is the data-access layer for family state. It wraps the project's
// *store.DB; it does not own the connection (callers manage Open/Close).
type Store struct {
	db *store.DB
}

// NewStore wraps a *store.DB. The caller is responsible for having run
// the migration (Open() does this automatically).
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// Category is one row of family_fact_categories.
type Category struct {
	Name         string
	Description  string
	AlwaysInject bool
	IsBuiltin    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Fact is one row of family_facts.
type Fact struct {
	ID         int64
	Category   string
	Subject    string
	Label      string
	Value      string
	Recurrence string // empty == NULL in DB
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListCategories returns every row in family_fact_categories ordered by name.
func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT name, description, always_inject, is_builtin, created_at, updated_at
		 FROM family_fact_categories ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var out []Category
	for rows.Next() {
		var c Category
		var alwaysInject, isBuiltin int
		var createdAt, updatedAt int64
		if err := rows.Scan(&c.Name, &c.Description, &alwaysInject, &isBuiltin, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		c.AlwaysInject = alwaysInject == 1
		c.IsBuiltin = isBuiltin == 1
		c.CreatedAt = time.Unix(createdAt, 0)
		c.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}
	return out, nil
}

// scanFact is shared by ListFacts and any future single-row reader.
func scanFact(rows *sql.Rows) (Fact, error) {
	var f Fact
	var recurrence sql.NullString
	var createdAt, updatedAt int64
	if err := rows.Scan(&f.ID, &f.Category, &f.Subject, &f.Label, &f.Value,
		&recurrence, &f.CreatedBy, &createdAt, &updatedAt); err != nil {
		return Fact{}, err
	}
	if recurrence.Valid {
		f.Recurrence = recurrence.String
	}
	f.CreatedAt = time.Unix(createdAt, 0)
	f.UpdatedAt = time.Unix(updatedAt, 0)
	return f, nil
}
```

- [ ] **Step 4: Run the test — it should pass**

Run: `go test ./internal/familystate/... -run TestListCategories_SeededBuiltins -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/familystate/store.go internal/familystate/store_test.go
git commit -m "feat(familystate): Store, Category, Fact types + ListCategories"
```

---

## Task 4 — `internal/familystate/store.go` UpsertCategory + DeleteCategory + UpsertFact + ListFacts + DeleteFact

**Files:**
- Modify: `internal/familystate/store.go` (append methods)
- Modify: `internal/familystate/store_test.go` (append tests)

- [ ] **Step 1: Append failing tests for fact CRUD**

Append to `internal/familystate/store_test.go`:

```go
func TestUpsertFact_RoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	want := Fact{
		Category:  "allergies",
		Subject:   "teo",
		Label:     "peanuts",
		Value:     "severe — EpiPen in Mom's purse",
		CreatedBy: "dep",
	}
	if err := s.UpsertFact(ctx, &want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if want.ID == 0 {
		t.Fatal("upsert did not populate ID")
	}

	got, err := s.ListFacts(ctx, FilterOpts{Category: "allergies"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d facts, want 1", len(got))
	}
	if got[0].Subject != want.Subject || got[0].Value != want.Value {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestUpsertFact_UnknownCategory(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.UpsertFact(context.Background(), &Fact{
		Category: "does_not_exist",
		Subject:  "teo",
		Label:    "x",
		Value:    "y",
		CreatedBy: "dep",
	})
	if !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("want ErrUnknownCategory, got %v", err)
	}
}

func TestDeleteCategory_BuiltinRefused(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.DeleteCategory(context.Background(), "allergies")
	if !errors.Is(err, ErrBuiltinCategory) {
		t.Errorf("want ErrBuiltinCategory, got %v", err)
	}
}

func TestDeleteCategory_NonEmptyRefused(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertCategory(ctx, &Category{Name: "custom1", Description: "test"}); err != nil {
		t.Fatalf("upsert category: %v", err)
	}
	if err := s.UpsertFact(ctx, &Fact{Category: "custom1", Subject: "family", Label: "a", Value: "b", CreatedBy: "dep"}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}
	err := s.DeleteCategory(ctx, "custom1")
	if !errors.Is(err, ErrCategoryNotEmpty) {
		t.Errorf("want ErrCategoryNotEmpty, got %v", err)
	}
}

func TestDeleteCategory_EmptyCustomOK(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertCategory(ctx, &Category{Name: "custom2", Description: "test"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteCategory(ctx, "custom2"); err != nil {
		t.Errorf("delete: %v", err)
	}
}
```

Add the import to the top of the test file:

```go
import (
	"context"
	"errors"
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)
```

- [ ] **Step 2: Run tests — should fail (methods don't exist yet)**

Run: `go test ./internal/familystate/... -v`
Expected: FAIL with "undefined: UpsertFact", "undefined: ListFacts", "undefined: UpsertCategory", "undefined: DeleteCategory", "undefined: FilterOpts".

- [ ] **Step 3: Append CRUD methods to store.go**

Append to `internal/familystate/store.go`:

```go
// FilterOpts narrows ListFacts. Zero-value lists every row.
type FilterOpts struct {
	Category string // exact match if set
	Subject  string // exact match if set
}

// UpsertCategory inserts or updates a row in family_fact_categories. is_builtin
// cannot be changed by callers — the seed migration is the only authoritative
// source. Description and always_inject ARE overwritten on conflict (this is
// the "parent edits a custom category" path).
func (s *Store) UpsertCategory(ctx context.Context, c *Category) error {
	now := time.Now().Unix()
	alwaysInject := 0
	if c.AlwaysInject {
		alwaysInject = 1
	}
	// New rows always get is_builtin=0; existing rows keep their current value.
	if _, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO family_fact_categories (name, description, always_inject, is_builtin, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   description   = excluded.description,
		   always_inject = excluded.always_inject,
		   updated_at    = excluded.updated_at`,
		c.Name, c.Description, alwaysInject, now, now); err != nil {
		return fmt.Errorf("upsert category: %w", err)
	}
	return nil
}

// DeleteCategory removes a custom category. Built-in categories cannot be
// deleted (ErrBuiltinCategory). A category with at least one referencing
// fact cannot be deleted (ErrCategoryNotEmpty — FK RESTRICT).
func (s *Store) DeleteCategory(ctx context.Context, name string) error {
	var isBuiltin int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT is_builtin FROM family_fact_categories WHERE name = ?`, name).Scan(&isBuiltin)
	if err == sql.ErrNoRows {
		return ErrUnknownCategory
	}
	if err != nil {
		return fmt.Errorf("delete category lookup: %w", err)
	}
	if isBuiltin == 1 {
		return ErrBuiltinCategory
	}
	res, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_fact_categories WHERE name = ? AND is_builtin = 0`, name)
	if err != nil {
		// FK RESTRICT failure surfaces here.
		return fmt.Errorf("delete category: %w", ErrCategoryNotEmpty)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownCategory
	}
	return nil
}

// UpsertFact inserts or updates a fact row. The category must exist (FK).
// UNIQUE(category, subject, label) determines upsert key; value/recurrence/
// updated_at overwritten on conflict. CreatedBy is set only on insert; it is
// preserved across updates.
func (s *Store) UpsertFact(ctx context.Context, f *Fact) error {
	// Pre-flight: better error message than the FK driver text.
	var exists int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT 1 FROM family_fact_categories WHERE name = ?`, f.Category).Scan(&exists); err == sql.ErrNoRows {
		return ErrUnknownCategory
	} else if err != nil {
		return fmt.Errorf("upsert fact category check: %w", err)
	}

	now := time.Now().Unix()
	var rec sql.NullString
	if f.Recurrence != "" {
		rec = sql.NullString{String: f.Recurrence, Valid: true}
	}
	res, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO family_facts (category, subject, label, value, recurrence, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(category, subject, label) DO UPDATE SET
		   value      = excluded.value,
		   recurrence = excluded.recurrence,
		   updated_at = excluded.updated_at
		 RETURNING id`,
		f.Category, f.Subject, f.Label, f.Value, rec, f.CreatedBy, now, now)
	if err != nil {
		return fmt.Errorf("upsert fact: %w", err)
	}
	// modernc.org/sqlite supports RETURNING via Result.LastInsertId only on plain INSERT;
	// for ON CONFLICT UPDATE we re-select.
	if id, _ := res.LastInsertId(); id != 0 {
		f.ID = id
	} else {
		if err := s.db.SQL().QueryRowContext(ctx,
			`SELECT id FROM family_facts WHERE category=? AND subject=? AND label=?`,
			f.Category, f.Subject, f.Label).Scan(&f.ID); err != nil {
			return fmt.Errorf("upsert fact reselect: %w", err)
		}
	}
	f.CreatedAt = time.Unix(now, 0)
	f.UpdatedAt = time.Unix(now, 0)
	return nil
}

// ListFacts returns facts ordered by category, subject, label.
func (s *Store) ListFacts(ctx context.Context, opts FilterOpts) ([]Fact, error) {
	q := `SELECT id, category, subject, label, value, recurrence, created_by, created_at, updated_at
	      FROM family_facts WHERE 1=1`
	var args []any
	if opts.Category != "" {
		q += ` AND category = ?`
		args = append(args, opts.Category)
	}
	if opts.Subject != "" {
		q += ` AND subject = ?`
		args = append(args, opts.Subject)
	}
	q += ` ORDER BY category, subject, label`

	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFact removes one fact row by id. Returns nil if the row didn't exist.
func (s *Store) DeleteFact(ctx context.Context, id int64) error {
	if _, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM family_facts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete fact: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests — should pass**

Run: `go test ./internal/familystate/... -v`
Expected: PASS for all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/familystate/store.go internal/familystate/store_test.go
git commit -m "feat(familystate): UpsertCategory, DeleteCategory, UpsertFact, ListFacts, DeleteFact"
```

---

## Task 5 — `internal/familystate/snapshot.go` Snapshot + Render + UnavailableSnapshot

**Files:**
- Create: `internal/familystate/snapshot.go`
- Create: `internal/familystate/snapshot_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/familystate/snapshot_test.go`:

```go
package familystate

import (
	"context"
	"strings"
	"testing"
)

func TestSnapshot_EmptyIsEmpty(t *testing.T) {
	s := &Snapshot{}
	if !s.IsEmpty() {
		t.Error("zero-value Snapshot should be IsEmpty")
	}
	if s.Render() != "" {
		t.Errorf("empty Snapshot Render = %q, want empty", s.Render())
	}
}

func TestSnapshot_RenderIncludesTagWrapper(t *testing.T) {
	s := &Snapshot{
		InjectedByCategory: map[string][]Fact{
			"allergies": {
				{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe — EpiPen in Mom's purse"},
			},
			"dietary_restrictions": {
				{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher household"},
				{Category: "dietary_restrictions", Subject: "julia", Label: "vegetarian", Value: "vegetarian"},
			},
		},
	}
	out := s.Render()
	if !strings.HasPrefix(out, "<family_safety>\n") {
		t.Errorf("Render must start with <family_safety>\\n; got %q", out[:min(40, len(out))])
	}
	if !strings.HasSuffix(out, "\n</family_safety>") {
		t.Errorf("Render must end with \\n</family_safety>; got tail %q", out[max(0, len(out)-40):])
	}
	// Ordering: allergies before dietary_restrictions (alphabetical). Family before teo within a category.
	allergiesIdx := strings.Index(out, "Allergies")
	dietIdx := strings.Index(out, "Dietary")
	if allergiesIdx == -1 || dietIdx == -1 || allergiesIdx > dietIdx {
		t.Errorf("category ordering wrong: allergies=%d dietary=%d in %q", allergiesIdx, dietIdx, out)
	}
}

func TestSnapshot_UnavailableHasNotice(t *testing.T) {
	s := UnavailableSnapshot()
	out := s.Render()
	if !strings.Contains(out, "safety context temporarily unavailable") {
		t.Errorf("UnavailableSnapshot.Render missing notice: %q", out)
	}
	if !strings.HasPrefix(out, "<family_safety>") || !strings.HasSuffix(out, "</family_safety>") {
		t.Errorf("UnavailableSnapshot.Render missing tag wrapper: %q", out)
	}
	if s.IsEmpty() {
		t.Error("UnavailableSnapshot should NOT be IsEmpty (it has a notice to render)")
	}
}

func TestAlwaysInjectedSnapshot_SkipsNonInjectedAndOrphans(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// Two safety facts (always_inject=1) + one pets fact (always_inject=0).
	for _, f := range []Fact{
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
		{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher", CreatedBy: "dep"},
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat", CreatedBy: "dep"},
		// orphan — subject 'ghost' not in our knownSubjects below.
		{Category: "allergies", Subject: "ghost", Label: "x", Value: "y", CreatedBy: "dep"},
	} {
		if err := store.UpsertFact(ctx, &f); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	known := map[string]bool{"teo": true, "family": true, "julia": true}
	snap, err := store.AlwaysInjectedSnapshot(ctx, known)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Should include allergies (teo) + dietary_restrictions (family). NOT pets (not injected).
	// NOT the ghost row (orphan).
	allergyRows := snap.InjectedByCategory["allergies"]
	if len(allergyRows) != 1 || allergyRows[0].Subject != "teo" {
		t.Errorf("allergies = %+v, want 1 row for teo", allergyRows)
	}
	if _, ok := snap.InjectedByCategory["pets"]; ok {
		t.Error("pets should not appear in always-injected snapshot")
	}
}
```

Add `min` and `max` helpers below (Go 1.22 has builtins, but older versions need shims — Go 1.22 is the project minimum so they're builtins). If your editor flags them, just remove the helpers — Go 1.22 ships `min`/`max` for ordered types.

- [ ] **Step 2: Run — should fail (Snapshot, IsEmpty, Render, UnavailableSnapshot, AlwaysInjectedSnapshot undefined)**

Run: `go test ./internal/familystate/... -run TestSnapshot -v`
Expected: FAIL.

- [ ] **Step 3: Write snapshot.go**

Create `internal/familystate/snapshot.go`:

```go
package familystate

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Snapshot is a read-only view of always-inject family facts, scoped
// to the families' valid subjects. Built by AlwaysInjectedSnapshot.
// A nil Snapshot is treated as empty by callers.
type Snapshot struct {
	// InjectedByCategory maps category name → facts for that category.
	// Keys are categories whose always_inject column is 1.
	InjectedByCategory map[string][]Fact

	// unavailable is set by UnavailableSnapshot() and renders a
	// "safety context temporarily unavailable" notice instead of facts.
	unavailable bool
}

// UnavailableSnapshot returns the sentinel value used when the snapshot
// DB read fails. memoryComponent renders this as a system-level notice
// so the model knows it is operating without safety context (R3 council
// 2-branch fail-stance).
func UnavailableSnapshot() *Snapshot {
	return &Snapshot{unavailable: true}
}

// IsEmpty reports whether the snapshot would render to nothing. The
// unavailable sentinel is NOT empty (it has a notice to render).
func (s *Snapshot) IsEmpty() bool {
	if s == nil {
		return true
	}
	if s.unavailable {
		return false
	}
	for _, rows := range s.InjectedByCategory {
		if len(rows) > 0 {
			return false
		}
	}
	return true
}

const unavailableNotice = "<family_safety>\nsafety context temporarily unavailable — operating with reduced family context\n</family_safety>"

// Render produces the system-prompt-ready block. The opening and
// closing <family_safety> tags are always present when Render returns
// a non-empty string. Ordering is deterministic: categories alpha,
// subjects alpha within each (with 'family' first), labels alpha
// within each subject. Snapshot tests pin this output exactly.
func (s *Snapshot) Render() string {
	if s == nil {
		return ""
	}
	if s.unavailable {
		return unavailableNotice
	}
	if s.IsEmpty() {
		return ""
	}

	cats := make([]string, 0, len(s.InjectedByCategory))
	for c := range s.InjectedByCategory {
		if len(s.InjectedByCategory[c]) == 0 {
			continue
		}
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var b strings.Builder
	b.WriteString("<family_safety>\n")
	for _, c := range cats {
		rows := s.InjectedByCategory[c]
		// Sort: family first, then subjects alpha, then labels alpha within each subject.
		sort.SliceStable(rows, func(i, j int) bool {
			si, sj := rows[i].Subject, rows[j].Subject
			if si != sj {
				if si == "family" {
					return true
				}
				if sj == "family" {
					return false
				}
				return si < sj
			}
			return rows[i].Label < rows[j].Label
		})

		label := categoryDisplayLabel(c)
		fmt.Fprintf(&b, "- %s:", label)
		// One fact per ", " segment. Subject — label (value).
		for i, f := range rows {
			sep := " "
			if i > 0 {
				sep = ". "
			}
			fmt.Fprintf(&b, "%s%s — %s (%s)", sep, displaySubject(f.Subject), f.Label, f.Value)
		}
		b.WriteString(".\n")
	}
	b.WriteString("</family_safety>")
	return b.String()
}

// categoryDisplayLabel makes "allergies" → "Allergies",
// "dietary_restrictions" → "Dietary restrictions".
func categoryDisplayLabel(name string) string {
	if name == "" {
		return name
	}
	words := strings.Split(name, "_")
	// Title-case only the first word; keep the rest lowercase.
	if len(words[0]) > 0 {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}

// displaySubject capitalizes the first letter for prompt readability.
// "family" stays "Family", "teo" stays "Teo".
func displaySubject(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// AlwaysInjectedSnapshot reads every fact whose category has always_inject=1
// AND whose subject is in knownSubjects (i.e. validated against config.Users
// names ∪ {"family"}). Orphan rows (unknown subject) are excluded from the
// snapshot AND logged via slog.Warn so a renamed user does not silently
// produce misattributed context (R3 council drift lock).
func (s *Store) AlwaysInjectedSnapshot(ctx context.Context, knownSubjects map[string]bool) (*Snapshot, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT f.id, f.category, f.subject, f.label, f.value, f.recurrence, f.created_by, f.created_at, f.updated_at
		FROM family_facts f
		JOIN family_fact_categories c ON c.name = f.category
		WHERE c.always_inject = 1
		ORDER BY f.category, f.subject, f.label`)
	if err != nil {
		return nil, fmt.Errorf("snapshot query: %w", err)
	}
	defer rows.Close()

	out := &Snapshot{InjectedByCategory: make(map[string][]Fact)}
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("snapshot scan: %w", err)
		}
		if !knownSubjects[f.Subject] {
			slog.Warn("family_facts: subject not in config, skipping",
				"subject", f.Subject, "category", f.Category, "id", f.ID)
			continue
		}
		out.InjectedByCategory[f.Category] = append(out.InjectedByCategory[f.Category], f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot iterate: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run all familystate tests — should pass**

Run: `go test -count=1 ./internal/familystate/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/familystate/snapshot.go internal/familystate/snapshot_test.go
git commit -m "feat(familystate): Snapshot, Render, UnavailableSnapshot, AlwaysInjectedSnapshot"
```

---

## Task 6 — `internal/familystate/proposal.go` JSON envelope

**Files:**
- Create: `internal/familystate/proposal.go`
- Create: `internal/familystate/proposal_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/familystate/proposal_test.go`:

```go
package familystate

import (
	"testing"
)

func TestProposalEnvelope_RoundTrip(t *testing.T) {
	want := Proposal{
		Kind:       "family_fact_proposal",
		Category:   "user_preferences",
		Subject:    "teo",
		Label:      "favorite_pizza",
		Value:      "pepperoni",
		Reason:     "Teo said so in chat",
		ProposedBy: "teo",
	}
	enc, err := EncodeProposal(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeProposal(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestProposalEnvelope_RejectsWrongKind(t *testing.T) {
	// Approvals rows for content-approval (not family_fact_proposal) must not
	// decode as a Proposal — the kind field is the discriminator.
	enc := `{"kind":"content_approval","query":"hi"}`
	_, err := DecodeProposal([]byte(enc))
	if err == nil {
		t.Error("DecodeProposal should reject non-family_fact_proposal kinds")
	}
}
```

- [ ] **Step 2: Run — should fail**

Run: `go test ./internal/familystate/... -run TestProposalEnvelope -v`
Expected: FAIL.

- [ ] **Step 3: Write proposal.go**

Create `internal/familystate/proposal.go`:

```go
package familystate

import (
	"encoding/json"
	"fmt"
)

// ProposalKind is the discriminator stored in approvals.query_text JSON
// envelopes. Approvals with this kind are family_fact proposals; the
// approve_request handler dispatches on this value to call UpsertFact.
const ProposalKind = "family_fact_proposal"

// Proposal is the JSON envelope written into approvals.query_text when
// a child calls propose_family_fact. The existing approvals table stays
// as-is; only the payload schema is new (R3 council: no new table, just
// dispatch on kind in the existing approval handler).
type Proposal struct {
	Kind       string `json:"kind"` // must equal ProposalKind for decode to succeed
	Category   string `json:"category"`
	Subject    string `json:"subject"`
	Label      string `json:"label"`
	Value      string `json:"value"`
	Reason     string `json:"reason,omitempty"`
	ProposedBy string `json:"proposed_by"`
}

// EncodeProposal serializes a Proposal to the JSON form stored in
// approvals.query_text. Always sets Kind = ProposalKind.
func EncodeProposal(p Proposal) ([]byte, error) {
	p.Kind = ProposalKind
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode proposal: %w", err)
	}
	return b, nil
}

// DecodeProposal parses the JSON envelope. Returns an error if the
// envelope's kind != ProposalKind — callers should only invoke this on
// approvals rows that already carry the family_fact_proposal category
// marker, but this guard keeps the decoder honest.
func DecodeProposal(data []byte) (Proposal, error) {
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal: %w", err)
	}
	if p.Kind != ProposalKind {
		return Proposal{}, fmt.Errorf("decode proposal: wrong kind %q (want %q)", p.Kind, ProposalKind)
	}
	return p, nil
}
```

- [ ] **Step 4: Run tests — should pass**

Run: `go test ./internal/familystate/... -v`
Expected: PASS (all tests, including prior).

- [ ] **Step 5: Commit**

```bash
git add internal/familystate/proposal.go internal/familystate/proposal_test.go
git commit -m "feat(familystate): JSON envelope encode/decode for kid proposals"
```

---

## Task 7 — Extend OPA `tool_policy.rego` + tests

**Files:**
- Modify: `internal/policy/policies/family/tool_policy.rego`
- Modify: `internal/policy/policies/family/tool_policy_test.rego`

- [ ] **Step 1: Add the new admin tools to the admin_tools set**

Edit `internal/policy/policies/family/tool_policy.rego`. Locate the `admin_tools := { ... }` block and extend:

```rego
admin_tools := {
    "list_pending_approvals",
    "approve_request",
    "deny_request",
    "list_users",
    "set_user_role",
    "list_unknown_accounts",
    "link_account",
    # Phase 3.3 mutations:
    "set_family_fact",
    "delete_family_fact",
    "add_family_category",
    "delete_family_category",
    # Synthetic check fired by the propose_family_fact handler when caller is parent.
    # Closes the "OPA hole" identified by R3 council — without this, a Go bug
    # could let a child auto-apply via the propose_family_fact path.
    "family_fact_proposal_auto_apply",
}
```

- [ ] **Step 2: Add OPA unit tests**

Append to `internal/policy/policies/family/tool_policy_test.rego`:

```rego
# ── Phase 3.3 family_state tests ──────────────────────────────────────────────

test_parent_set_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "set_family_fact"
    }
}

test_child_no_set_family_fact if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "set_family_fact"
    }
}

test_parent_delete_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "delete_family_fact"
    }
}

test_child_no_delete_family_fact if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "delete_family_fact"
    }
}

test_parent_add_family_category if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "add_family_category"
    }
}

test_child_no_add_family_category if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "under_8"},
        "tool_name": "add_family_category"
    }
}

test_parent_delete_family_category if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "delete_family_category"
    }
}

test_child_no_delete_family_category if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "delete_family_category"
    }
}

# get_family_state and propose_family_fact must be callable by all roles.

test_parent_get_family_state if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "get_family_state"
    }
}

test_child_get_family_state if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_8_12"},
        "tool_name": "get_family_state"
    }
}

test_parent_propose_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "propose_family_fact"
    }
}

test_child_propose_family_fact if {
    tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "propose_family_fact"
    }
}

# OPA hole closure: synthetic auto-apply name is parent-only.

test_parent_auto_apply_allowed if {
    tool_policy.allow with input as {
        "user": {"role": "parent", "age_group": ""},
        "tool_name": "family_fact_proposal_auto_apply"
    }
}

test_child_auto_apply_denied if {
    not tool_policy.allow with input as {
        "user": {"role": "child", "age_group": "age_13_17"},
        "tool_name": "family_fact_proposal_auto_apply"
    }
}
```

- [ ] **Step 3: Run OPA tests**

Run: `opa test internal/policy/policies/family/ internal/policy/policies/data/ -v`
Expected: PASS. New tests should show in output (`test_parent_set_family_fact`, etc.).

- [ ] **Step 4: Run Go tests on the policy package**

Run: `go test ./internal/policy/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/policy/policies/family/tool_policy.rego internal/policy/policies/family/tool_policy_test.rego
git commit -m "feat(policy): OPA gates for Phase 3.3 family_state tools + auto-apply"
```

---

## Task 8 — Prompt builder: extend BuildContext + flip memoryComponent

**Files:**
- Modify: `internal/prompt/builder.go`
- Modify: `internal/prompt/components.go`
- Create: `internal/prompt/testdata/age_13_17_with_safety.snap`

- [ ] **Step 1: Extend BuildContext**

Edit `internal/prompt/builder.go`. Replace the `BuildContext` struct definition with:

```go
type BuildContext struct {
	Cfg          *config.Config     // family config (users list, etc.)
	User         *config.UserConfig // the user this prompt is for
	Gateway      string             // "telegram" | "discord" | "web" | ""
	Skills       []string           // skill names loaded for this user; can be empty
	HardBlocked  []string           // hard-blocked policy categories for this user
	BuiltinTools []string           // builtin tool bare names (e.g. "spawn_agent", "web_fetch")
	FamilyState  *familystate.Snapshot // Phase 3.3 — may be nil (legacy callers) or UnavailableSnapshot
}
```

Add the import:

```go
import (
	"strings"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
)
```

- [ ] **Step 2: Flip memoryComponent on**

Edit `internal/prompt/components.go`. Replace the existing `memoryComponent` with:

```go
// memoryComponent renders the always-injected family-state safety block,
// or the "safety context unavailable" notice if the snapshot read failed.
// Phase 3.3 flip — was a placeholder returning ("", false) until now.
func memoryComponent(c BuildContext) (string, bool) {
	if c.FamilyState == nil || c.FamilyState.IsEmpty() {
		return "", false
	}
	return c.FamilyState.Render(), true
}
```

- [ ] **Step 3: Run prompt unit tests to confirm nothing broke**

Run: `go test ./internal/prompt/... -v`
Expected: existing tests PASS (FamilyState is optional; nil → component skipped).

- [ ] **Step 4: Add a snapshot test asserting the safety block reaches the output**

Append to `internal/prompt/snapshot_test.go`:

```go
func TestSnapshot_Age13_17_WithSafety(t *testing.T) {
	cfg := minimalConfig()
	user := &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_13_17", DisplayName: "Teo"}

	snap := &familystate.Snapshot{
		InjectedByCategory: map[string][]familystate.Fact{
			"allergies": {{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe — EpiPen in Mom's purse"}},
			"dietary_restrictions": {
				{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher household"},
				{Category: "dietary_restrictions", Subject: "julia", Label: "vegetarian", Value: "vegetarian"},
			},
		},
	}

	got := Build(BuildContext{Cfg: cfg, User: user, Gateway: "discord", FamilyState: snap})
	assertSnapshot(t, "age_13_17_with_safety", got)
}
```

Add imports if missing:

```go
import (
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
)
```

(If `minimalConfig()` or `assertSnapshot()` don't already exist in the test file, copy the pattern from the existing `snapshot_test.go` — they should already be there since the snapshot tests for parent/age_8_12/under_8 already use them.)

- [ ] **Step 5: Run the new test once to generate the snapshot file**

Run: `UPDATE_SNAPSHOTS=1 go test ./internal/prompt/... -run TestSnapshot_Age13_17_WithSafety -v`
(If the snapshot package uses a different env var, check `snapshot_test.go` for the convention — common ones are `UPDATE_SNAPSHOTS=1` or `-update`.)
Expected: PASS, and `internal/prompt/testdata/age_13_17_with_safety.snap` is created.

- [ ] **Step 6: Inspect the snapshot file**

Open `internal/prompt/testdata/age_13_17_with_safety.snap`. It should contain the assembled system prompt including the `<family_safety>` block. Verify the block looks reasonable (correct facts, tag wrapper present, no stray whitespace).

- [ ] **Step 7: Re-run without UPDATE_SNAPSHOTS to confirm it stays stable**

Run: `go test ./internal/prompt/... -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/prompt/builder.go internal/prompt/components.go internal/prompt/snapshot_test.go internal/prompt/testdata/age_13_17_with_safety.snap
git commit -m "feat(prompt): flip memoryComponent on + add age_13_17 safety snapshot"
```

---

## Task 9 — Agent wiring: familyState field + snapshot-at-build-time

**Files:**
- Modify: `internal/agent/agent.go`

- [ ] **Step 1: Add the familyState field**

Edit the `Agent` struct definition (around line 42):

```go
type Agent struct {
	user         *config.UserConfig
	cfg          *config.Config
	llmClient    llm.Chatter
	evaluator    *policy.Evaluator
	classifier   *classifier.Classifier
	db           *store.DB
	pool         *mcp.Pool
	skills       []*skillbridge.Skill
	quarantine   *skillbridge.Quarantine
	scanner      skillbridge.Scanner
	scheduler    *subagent.Scheduler
	builtinTools []agentcore.Tool
	convID       string
	gateway      string
	familyState  *familystate.Store // Phase 3.3 — nil disables family-state injection
}
```

- [ ] **Step 2: Add the wire-up in New()**

Locate the `func New(...)` constructor. Add `familyState` initialization where other fields are wired:

```go
	a := &Agent{
		// ... existing fields ...
		familyState: familystate.NewStore(db),
	}
```

Add the import at the top of `agent.go`:

```go
	"github.com/famclaw/famclaw/internal/familystate"
```

- [ ] **Step 3: Add the snapshot call before prompt.Build**

Locate the `prompt.Build(prompt.BuildContext{...})` call (around line 713). Replace it with:

```go
		// Phase 3.3 — load the always-injected family-state snapshot.
		// On any non-nil error, use the UnavailableSnapshot sentinel so the
		// model gets a "safety context temporarily unavailable" notice
		// rather than silently dropping the block (R3 council 2-branch lock).
		var snap *familystate.Snapshot
		if a.familyState != nil {
			known := knownSubjects(a.cfg)
			s, err := a.familyState.AlwaysInjectedSnapshot(ctx, known)
			if err != nil {
				slog.ErrorContext(ctx, "family-state snapshot failed; injecting unavailable notice", "err", err)
				snap = familystate.UnavailableSnapshot()
			} else {
				snap = s
			}
		}

		systemPrompt = prompt.Build(prompt.BuildContext{
			Cfg:          a.cfg,
			User:         a.user,
			Skills:       skillNames,
			BuiltinTools: builtinNames,
			FamilyState:  snap,
		})
```

- [ ] **Step 4: Add the knownSubjects helper**

Append to `agent.go` (anywhere near other helpers like `parseStringList`):

```go
// knownSubjects builds the set of valid subjects for family-state rows:
// every config.Users[].Name plus the literal "family".
func knownSubjects(cfg *config.Config) map[string]bool {
	out := map[string]bool{"family": true}
	if cfg == nil {
		return out
	}
	for _, u := range cfg.Users {
		out[u.Name] = true
	}
	return out
}
```

Add the `log/slog` import if it's not already present.

- [ ] **Step 5: Build and run agent tests**

Run: `go build ./internal/agent/...`
Expected: clean.

Run: `go test -count=1 ./internal/agent/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/agent.go
git commit -m "feat(agent): wire familystate.Snapshot into prompt build with unavailable fallback"
```

---

## Task 10 — Builtin tool: `get_family_state` (read, all-roles)

**Files:**
- Modify: `internal/agent/agent.go` (add tool def + dispatch case + handler)

- [ ] **Step 1: Add the tool definition**

Find where `web_fetch` or `tool_result_more` tool definitions are constructed (probably in a `builtinToolDefs()` or similar helper). Add a sibling definition:

```go
func getFamilyStateToolDef() agentcore.Tool {
	return agentcore.Tool{
		Name: "builtin__get_family_state",
		Description: strings.Join([]string{
			"Read the family's stored facts: pets, important_dates, allergies, dietary_restrictions, and any custom categories the parents have added.",
			"",
			"WHEN TO USE: the user asks something where family-specific knowledge matters — pet name, family member's birthday, what foods are kept in the house, who has what allergy.",
			"WHEN NOT TO USE: generic questions with no family-specific component. Pure factual lookups (weather, news) belong in web_fetch.",
			"",
			"Example call: get_family_state(category=\"pets\") to list pets, or get_family_state() with no category to see everything in a single response.",
		}, "\n"),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"description": "Optional category filter — e.g. 'pets', 'important_dates', 'allergies'. Omit to read every category.",
				},
			},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child"},
	}
}
```

Register it in whatever function assembles the parent's `builtinTools` slice (mirror how `web_fetch` is added).

- [ ] **Step 2: Add the dispatch case**

In `makeBuiltinHandler` (around line 295), add a case in the switch:

```go
		case "builtin__get_family_state":
			return a.handleGetFamilyState(ctx, args)
```

- [ ] **Step 3: Add the handler**

Append to `agent.go`:

```go
// handleGetFamilyState reads family_facts (optionally filtered to one category)
// and returns a rendered text block grouped by category. Open to all roles
// (no admin_tools gating). The render format mirrors Snapshot.Render's style
// but is NOT wrapped in <family_safety> tags — that wrapper is reserved for
// the always-injected path.
func (a *Agent) handleGetFamilyState(ctx context.Context, args map[string]any) (string, error) {
	if a.familyState == nil {
		return "Family state is not configured.", nil
	}
	category, _ := args["category"].(string)

	facts, err := a.familyState.ListFacts(ctx, familystate.FilterOpts{Category: category})
	if err != nil {
		return "", fmt.Errorf("get_family_state: %w", err)
	}
	if len(facts) == 0 {
		if category != "" {
			return fmt.Sprintf("No facts in category %q.", category), nil
		}
		return "No family facts have been recorded yet.", nil
	}

	// Group by category for readability.
	known := knownSubjects(a.cfg)
	byCat := map[string][]familystate.Fact{}
	for _, f := range facts {
		if !known[f.Subject] {
			// Skip orphans here too — the dashboard exposes them separately.
			continue
		}
		byCat[f.Category] = append(byCat[f.Category], f)
	}
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var b strings.Builder
	for _, c := range cats {
		fmt.Fprintf(&b, "%s:\n", c)
		for _, f := range byCat[c] {
			fmt.Fprintf(&b, "  - %s — %s: %s\n", f.Subject, f.Label, f.Value)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
```

Add `"sort"` import if not present.

- [ ] **Step 4: Write a handler test**

Append to `internal/agent/agent_test.go` (or create one — follow existing pattern; look at where `handleWebFetch` or `handleToolResultMore` tests live):

```go
func TestHandleGetFamilyState_RendersAllRows(t *testing.T) {
	// Setup: open a fresh in-memory store, seed a couple of facts via familystate.Store,
	// then construct a minimal Agent and call handleGetFamilyState.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fs := familystate.NewStore(db)
	ctx := context.Background()
	for _, f := range []familystate.Fact{
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat, age 5", CreatedBy: "dep"},
		{Category: "pets", Subject: "family", Label: "Rex", Value: "dog, age 3", CreatedBy: "dep"},
	} {
		if err := fs.UpsertFact(ctx, &f); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cfg := &config.Config{Users: []config.UserConfig{{Name: "dep", Role: "parent"}}}
	a := &Agent{cfg: cfg, familyState: fs, user: &cfg.Users[0]}

	out, err := a.handleGetFamilyState(ctx, map[string]any{"category": "pets"})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !strings.Contains(out, "Stella") || !strings.Contains(out, "Rex") {
		t.Errorf("output missing pet names: %q", out)
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test -count=1 ./internal/agent/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_test.go
git commit -m "feat(agent): builtin__get_family_state tool (read, all roles)"
```

---

## Task 11 — Admin tools: set_family_fact, delete_family_fact, add_family_category, delete_family_category

**Files:**
- Create: `internal/agent/tools/admin/set_family_fact.go`
- Create: `internal/agent/tools/admin/delete_family_fact.go`
- Create: `internal/agent/tools/admin/add_family_category.go`
- Create: `internal/agent/tools/admin/delete_family_category.go`
- Modify: `internal/agent/tools/admin/tool.go` (extend Deps + registration)

- [ ] **Step 1: Extend Deps to carry familyState**

Edit `internal/agent/tools/admin/tool.go`. Update Deps:

```go
type Deps struct {
	DB          *store.DB
	Cfg         *config.Config
	Actor       string
	Gateway     string
	Gateways    map[string]GatewaySender
	FamilyState *familystate.Store // Phase 3.3 — required for family_* admin tools
}
```

Add import:

```go
	"github.com/famclaw/famclaw/internal/familystate"
```

- [ ] **Step 2: Write set_family_fact.go**

Create `internal/agent/tools/admin/set_family_fact.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/familystate"
)

const toolNameSetFamilyFact = "builtin__set_family_fact"

func SetFamilyFactDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameSetFamilyFact,
		Description: "Add or update a family fact (parent-only). " +
			"Use to record allergies, dietary restrictions, important dates, pet info, or anything in a custom category. " +
			"Example: set_family_fact(category=\"allergies\", subject=\"teo\", label=\"peanuts\", value=\"severe — EpiPen in Mom's purse\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string", "description": "Category name, e.g. 'allergies'."},
				"subject":  map[string]any{"type": "string", "description": "Username from config or the literal 'family'."},
				"label":    map[string]any{"type": "string", "description": "The specific item: 'peanuts', 'Stella', 'Saturday'."},
				"value":    map[string]any{"type": "string", "description": "Free-form details about the labelled item."},
			},
			"required": []string{"category", "subject", "label", "value"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

func HandleSetFamilyFact(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("set_family_fact: family state is not configured")
	}
	category, _ := args["category"].(string)
	subject, _ := args["subject"].(string)
	label, _ := args["label"].(string)
	value, _ := args["value"].(string)

	if category == "" || subject == "" || label == "" || value == "" {
		return "", fmt.Errorf("set_family_fact: category, subject, label, and value are all required")
	}
	if len(label) > 64 {
		return "label too long (max 64 chars)", nil
	}
	if len(value) > 512 {
		return "value too long (max 512 chars)", nil
	}
	// Subject validation against config.
	if !isKnownSubject(deps.Cfg, subject) {
		return fmt.Sprintf("subject %q is not a family member", subject), nil
	}

	f := familystate.Fact{
		Category:  category,
		Subject:   subject,
		Label:     label,
		Value:     value,
		CreatedBy: deps.Actor,
	}
	if err := deps.FamilyState.UpsertFact(ctx, &f); err != nil {
		if errors.Is(err, familystate.ErrUnknownCategory) {
			return fmt.Sprintf("unknown category %q — a parent can create it via add_family_category", category), nil
		}
		return "", fmt.Errorf("set_family_fact: %w", err)
	}

	auditArgs, _ := json.Marshal(map[string]any{
		"category": category, "subject": subject, "label": label, "value": value, "id": f.ID,
	})
	if err := logAudit(ctx, deps, toolNameSetFamilyFact, json.RawMessage(auditArgs)); err != nil {
		fmt.Fprintf(stderr, "[admin] audit log failed: %v\n", err)
	}
	return fmt.Sprintf("ok — fact #%d", f.ID), nil
}

// isKnownSubject reports whether subject is in config.Users names or is the literal 'family'.
func isKnownSubject(cfg interface {
	// Type-safe alias to avoid importing config in this small helper signature.
}, subject string) bool {
	// Body filled by concrete type inside the package. See helper below.
	return _isKnownSubject(subject)
}

// _isKnownSubject is set by tool.go init — keeps helper definitions in one place.
var _isKnownSubject = func(string) bool { return false }
```

Actually that's clunky — drop the indirection and just do the lookup directly:

Replace the bottom of `set_family_fact.go` (the `isKnownSubject` block) with:

```go
// stderr is overridable for tests.
var stderr = os.Stderr
```

And add at the top:

```go
import (
	"os"
)
```

Then replace `isKnownSubject(deps.Cfg, subject)` with an inline check that uses `deps.Cfg.Users`:

```go
	known := false
	if subject == "family" {
		known = true
	} else if deps.Cfg != nil {
		for _, u := range deps.Cfg.Users {
			if u.Name == subject {
				known = true
				break
			}
		}
	}
	if !known {
		return fmt.Sprintf("subject %q is not a family member", subject), nil
	}
```

Remove the `isKnownSubject` / `_isKnownSubject` helpers entirely.

- [ ] **Step 3: Write the other three admin tools**

Create `internal/agent/tools/admin/delete_family_fact.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameDeleteFamilyFact = "builtin__delete_family_fact"

func DeleteFamilyFactDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameDeleteFamilyFact,
		Description: "Delete one family fact by its numeric id (parent-only). Use after list_family_facts shows the id you want to remove.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "The fact's numeric id."},
			},
			"required": []string{"id"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

func HandleDeleteFamilyFact(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("delete_family_fact: family state is not configured")
	}
	idF, ok := args["id"].(float64)
	if !ok {
		return "", fmt.Errorf("delete_family_fact: id must be a number")
	}
	id := int64(idF)
	if id <= 0 {
		return "", fmt.Errorf("delete_family_fact: id must be positive")
	}
	if err := deps.FamilyState.DeleteFact(ctx, id); err != nil {
		return "", fmt.Errorf("delete_family_fact: %w", err)
	}
	auditArgs, _ := json.Marshal(map[string]any{"id": id})
	_ = logAudit(ctx, deps, toolNameDeleteFamilyFact, json.RawMessage(auditArgs))
	return fmt.Sprintf("ok — fact #%d deleted (or did not exist)", id), nil
}
```

Create `internal/agent/tools/admin/add_family_category.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/familystate"
)

const toolNameAddFamilyCategory = "builtin__add_family_category"

var categoryNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)

func AddFamilyCategoryDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameAddFamilyCategory,
		Description: "Create a new family-fact category (parent-only). " +
			"Use to organize family-specific facts that don't fit the built-in categories (allergies, dietary_restrictions, important_dates, pets). " +
			"Example: add_family_category(name=\"movie_night\", description=\"recurring family activities\", always_inject=false)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Lower-case category name, [a-z0-9_]+, ≤ 32 chars."},
				"description":   map[string]any{"type": "string", "description": "Human-readable purpose of the category."},
				"always_inject": map[string]any{"type": "boolean", "description": "If true, facts in this category appear in every system prompt. Default false."},
			},
			"required": []string{"name", "description"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

func HandleAddFamilyCategory(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("add_family_category: family state is not configured")
	}
	name, _ := args["name"].(string)
	desc, _ := args["description"].(string)
	always, _ := args["always_inject"].(bool)

	if name == "" || desc == "" {
		return "", fmt.Errorf("add_family_category: name and description are required")
	}
	if len(name) > 32 || !categoryNameRE.MatchString(name) {
		return fmt.Sprintf("invalid category name %q — must be [a-z0-9_]+ and ≤ 32 chars", name), nil
	}
	if len(desc) > 256 {
		return "description too long (max 256 chars)", nil
	}

	cat := familystate.Category{Name: name, Description: desc, AlwaysInject: always}
	if err := deps.FamilyState.UpsertCategory(ctx, &cat); err != nil {
		return "", fmt.Errorf("add_family_category: %w", err)
	}
	auditArgs, _ := json.Marshal(map[string]any{"name": name, "always_inject": always})
	_ = logAudit(ctx, deps, toolNameAddFamilyCategory, json.RawMessage(auditArgs))
	return fmt.Sprintf("ok — category %q ready", name), nil
}
```

Create `internal/agent/tools/admin/delete_family_category.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/familystate"
)

const toolNameDeleteFamilyCategory = "builtin__delete_family_category"

func DeleteFamilyCategoryDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name:        toolNameDeleteFamilyCategory,
		Description: "Delete a custom family-fact category (parent-only). Built-in categories cannot be deleted. The category must be empty (delete its facts first).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Category name to delete."},
			},
			"required": []string{"name"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

func HandleDeleteFamilyCategory(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.FamilyState == nil {
		return "", fmt.Errorf("delete_family_category: family state is not configured")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("delete_family_category: name is required")
	}
	if err := deps.FamilyState.DeleteCategory(ctx, name); err != nil {
		switch {
		case errors.Is(err, familystate.ErrBuiltinCategory):
			return "can't delete a built-in category", nil
		case errors.Is(err, familystate.ErrCategoryNotEmpty):
			return fmt.Sprintf("category %q has facts; delete them first", name), nil
		case errors.Is(err, familystate.ErrUnknownCategory):
			return fmt.Sprintf("unknown category %q", name), nil
		default:
			return "", fmt.Errorf("delete_family_category: %w", err)
		}
	}
	auditArgs, _ := json.Marshal(map[string]any{"name": name})
	_ = logAudit(ctx, deps, toolNameDeleteFamilyCategory, json.RawMessage(auditArgs))
	return fmt.Sprintf("ok — category %q deleted", name), nil
}
```

- [ ] **Step 4: Register the four new tools in tool.go**

Edit `internal/agent/tools/admin/tool.go`. Update `AllMutatingDefinitions`:

```go
func AllMutatingDefinitions() []agentcore.Tool {
	return []agentcore.Tool{
		ApproveRequestDefinition(),
		DenyRequestDefinition(),
		SetUserRoleDefinition(),
		LinkAccountDefinition(),
		// Phase 3.3:
		SetFamilyFactDefinition(),
		DeleteFamilyFactDefinition(),
		AddFamilyCategoryDefinition(),
		DeleteFamilyCategoryDefinition(),
	}
}
```

- [ ] **Step 5: Wire the four new dispatch cases in agent.go**

Edit `internal/agent/agent.go`. In `makeBuiltinHandler`, after the existing admin cases:

```go
		case "builtin__set_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleSetFamilyFact(ctx, deps, args)
		case "builtin__delete_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleDeleteFamilyFact(ctx, deps, args)
		case "builtin__add_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleAddFamilyCategory(ctx, deps, args)
		case "builtin__delete_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleDeleteFamilyCategory(ctx, deps, args)
```

- [ ] **Step 6: Build and test**

Run: `go build ./...`
Expected: clean.

Run: `go test -count=1 ./internal/agent/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/tools/admin/ internal/agent/agent.go
git commit -m "feat(admin): set/delete fact + add/delete category tools (parent-only)"
```

---

## Task 12 — `propose_family_fact` tool with OPA auto-apply gate

**Files:**
- Modify: `internal/agent/agent.go` (tool def + dispatch case + handler)
- Modify: `internal/agent/tools/admin/approve_request.go` (kind dispatch)

- [ ] **Step 1: Add the propose_family_fact tool definition**

In the same place where `get_family_state`'s definition is registered, add:

```go
func proposeFamilyFactToolDef() agentcore.Tool {
	return agentcore.Tool{
		Name: "builtin__propose_family_fact",
		Description: strings.Join([]string{
			"Propose a new family fact. Anyone can call this.",
			"",
			"WHEN PARENT: the fact is applied immediately, same as set_family_fact (after an OPA policy check).",
			"WHEN CHILD: a proposal is sent to parents for approval; the fact is applied only after a parent approves.",
			"",
			"Example call: propose_family_fact(category=\"user_preferences\", subject=\"teo\", label=\"favorite_pizza\", value=\"pepperoni\", reason=\"asked in chat\")",
		}, "\n"),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string"},
				"subject":  map[string]any{"type": "string"},
				"label":    map[string]any{"type": "string"},
				"value":    map[string]any{"type": "string"},
				"reason":   map[string]any{"type": "string", "description": "Why this fact matters; helps the parent decide."},
			},
			"required": []string{"category", "subject", "label", "value"},
		},
		Source: "builtin",
		Roles:  []string{"parent", "child"},
	}
}
```

Register it alongside `get_family_state`.

- [ ] **Step 2: Add the dispatch case**

In `makeBuiltinHandler`:

```go
		case "builtin__propose_family_fact":
			return a.handleProposeFamilyFact(ctx, args)
```

- [ ] **Step 3: Write the handler with OPA auto-apply check**

Append to `agent.go`:

```go
// handleProposeFamilyFact accepts a fact proposal from any role.
// PARENTS get the auto-apply path AFTER a policy decision via the synthetic
// "family_fact_proposal_auto_apply" tool name (closes the R3 council "OPA
// hole" — authorization for the auto-apply branch is enforced by OPA, not
// Go alone). CHILDREN's proposals are written to the approvals table; the
// existing notify path fires; a parent calls approve_request to complete.
func (a *Agent) handleProposeFamilyFact(ctx context.Context, args map[string]any) (string, error) {
	if a.familyState == nil {
		return "", fmt.Errorf("propose_family_fact: family state is not configured")
	}
	category, _ := args["category"].(string)
	subject, _ := args["subject"].(string)
	label, _ := args["label"].(string)
	value, _ := args["value"].(string)
	reason, _ := args["reason"].(string)

	if category == "" || subject == "" || label == "" || value == "" {
		return "", fmt.Errorf("propose_family_fact: category, subject, label, and value are all required")
	}

	// PARENT path — auto-apply, but only if OPA permits the synthetic name.
	if a.user.Role == "parent" {
		dec, err := a.evaluator.EvaluateTool(ctx, policy.ToolInput{
			User:     policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup, Name: a.user.Name},
			ToolName: "family_fact_proposal_auto_apply",
		})
		if err != nil {
			return "", fmt.Errorf("propose_family_fact: opa: %w", err)
		}
		if !dec.Allow {
			return "Not authorized to auto-apply this proposal.", nil
		}
		f := familystate.Fact{
			Category: category, Subject: subject, Label: label, Value: value,
			CreatedBy: a.user.Name,
		}
		if err := a.familyState.UpsertFact(ctx, &f); err != nil {
			if errors.Is(err, familystate.ErrUnknownCategory) {
				return fmt.Sprintf("unknown category %q — create it with add_family_category first", category), nil
			}
			return "", fmt.Errorf("propose_family_fact upsert: %w", err)
		}
		auditArgs, _ := json.Marshal(map[string]any{
			"category": category, "subject": subject, "label": label, "value": value,
			"id": f.ID, "auto_apply_parent": true,
		})
		_ = a.db.LogAudit(ctx, a.user.Name, a.gateway, "builtin__propose_family_fact", auditArgs)
		return fmt.Sprintf("ok — fact #%d applied directly (parent auto-apply)", f.ID), nil
	}

	// CHILD path — write to approvals, fire notify.
	envelope, err := familystate.EncodeProposal(familystate.Proposal{
		Category: category, Subject: subject, Label: label, Value: value,
		Reason: reason, ProposedBy: a.user.Name,
	})
	if err != nil {
		return "", fmt.Errorf("propose_family_fact encode: %w", err)
	}
	approval := &store.Approval{
		ID:          uuid.NewString(),
		UserName:    a.user.Name,
		UserDisplay: a.user.DisplayName,
		AgeGroup:    a.user.AgeGroup,
		Category:    familystate.ProposalKind,
		QueryText:   string(envelope),
	}
	if _, err := a.db.UpsertApproval(approval); err != nil {
		return "", fmt.Errorf("propose_family_fact approval: %w", err)
	}
	// TODO(famclaw/issue-XXX): family_fact_proposal notification copy —
	// the existing notify.MultiNotifier formats text for content-approval
	// (e.g. "child asked about X"). For family_fact_proposal the copy
	// reads awkwardly but the approve/deny flow works end-to-end.
	if a.notifier != nil {
		// Best-effort; failure to notify must not abort the proposal.
		_ = a.notifier.Notify(ctx, approval, "", "")
	}
	return "Proposal sent to parents.", nil
}
```

Add the required imports if not already present (`errors`, `encoding/json`, `github.com/google/uuid`, and confirm `internal/policy` is imported).

- [ ] **Step 4: Update approve_request.go to dispatch on kind**

Edit `internal/agent/tools/admin/approve_request.go`. After the `DecideApprovalWithNote` call succeeds and `approval` is fetched, add:

```go
	// Phase 3.3 — if the approval is a family_fact_proposal, apply the fact now.
	if approval != nil && approval.Category == familystate.ProposalKind {
		if deps.FamilyState == nil {
			fmt.Fprintf(os.Stderr, "[admin] approve_request: family_fact_proposal but FamilyState is nil\n")
		} else {
			p, err := familystate.DecodeProposal([]byte(approval.QueryText))
			if err != nil {
				fmt.Fprintf(os.Stderr, "[admin] approve_request: decode proposal %s: %v\n", requestID, err)
			} else {
				f := familystate.Fact{
					Category: p.Category, Subject: p.Subject, Label: p.Label, Value: p.Value,
					CreatedBy: p.ProposedBy,
				}
				if err := deps.FamilyState.UpsertFact(ctx, &f); err != nil {
					fmt.Fprintf(os.Stderr, "[admin] approve_request: upsert family fact %s: %v\n", requestID, err)
				}
			}
		}
	}
```

Add the import:

```go
	"github.com/famclaw/famclaw/internal/familystate"
```

Note: `store.Approval` already has a `QueryText` field — confirm the type name matches; if it's `query_text` lowercase or different, adjust accordingly. (Look at `internal/store/db.go` Approval struct.)

- [ ] **Step 5: Run tests**

Run: `go build ./...`
Expected: clean.

Run: `go test -count=1 ./internal/agent/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/agent.go internal/agent/tools/admin/approve_request.go
git commit -m "feat(agent): propose_family_fact tool + approval dispatch on family_fact_proposal kind"
```

---

## Task 13 — Web dashboard handler + page

**Files:**
- Create: `internal/web/familystate_handler.go`
- Create: `internal/web/familystate_handler_test.go`
- Create: `internal/web/static/family-state.html`
- Modify: `internal/web/server.go` (route registration)

- [ ] **Step 1: Write a failing handler test**

Create `internal/web/familystate_handler_test.go`:

```go
package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/store"
)

func TestFamilyStatePost_ParentSession_Creates(t *testing.T) {
	db, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = db.Close() })
	fs := familystate.NewStore(db)

	srv := newTestServerWithFamilyState(t, db, fs, /*sessionAsParent=*/ true)

	body, _ := json.Marshal(map[string]any{
		"category": "pets", "subject": "family", "label": "Stella", "value": "cat",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/family-state/facts", bytes.NewReader(body))
	req.AddCookie(srv.parentCookie)
	rec := httptest.NewRecorder()

	srv.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	facts, _ := fs.ListFacts(req.Context(), familystate.FilterOpts{Category: "pets"})
	if len(facts) != 1 {
		t.Errorf("got %d facts, want 1", len(facts))
	}
}

func TestFamilyStatePost_NoSession_401(t *testing.T) {
	db, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServerWithFamilyState(t, db, familystate.NewStore(db), /*parent*/ false)

	req := httptest.NewRequest(http.MethodPost, "/api/family-state/facts", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
```

(The `newTestServerWithFamilyState` helper needs to mirror whatever helper the existing web tests use to wire up a Server with an authed session — check `internal/web/server_test.go` or similar for the established pattern and add the FamilyState wiring there.)

- [ ] **Step 2: Run the test — should fail (handler doesn't exist)**

Run: `go test ./internal/web/... -run TestFamilyStatePost -v`
Expected: FAIL.

- [ ] **Step 3: Write the handler**

Create `internal/web/familystate_handler.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/famclaw/famclaw/internal/familystate"
)

type familyStateFactRequest struct {
	Category string `json:"category"`
	Subject  string `json:"subject"`
	Label    string `json:"label"`
	Value    string `json:"value"`
}

// handleFamilyStateFacts dispatches GET (list) and POST (upsert) on /api/family-state/facts.
func (s *Server) handleFamilyStateFacts(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForRequest(r) // existing helper that resolves parent session
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.familyStateListFacts(w, r)
	case http.MethodPost:
		s.familyStateUpsertFact(w, r, sess.UserName)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) familyStateListFacts(w http.ResponseWriter, r *http.Request) {
	if s.familyState == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	opts := familystate.FilterOpts{Category: r.URL.Query().Get("category")}
	facts, err := s.familyState.ListFacts(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, facts)
}

func (s *Server) familyStateUpsertFact(w http.ResponseWriter, r *http.Request, actor string) {
	if s.familyState == nil {
		http.Error(w, "family state not configured", http.StatusServiceUnavailable)
		return
	}
	var req familyStateFactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Category == "" || req.Subject == "" || req.Label == "" || req.Value == "" {
		http.Error(w, "category, subject, label, value required", http.StatusBadRequest)
		return
	}
	if len(req.Label) > 64 || len(req.Value) > 512 {
		http.Error(w, "label or value too long", http.StatusBadRequest)
		return
	}
	// Subject validation
	known := false
	if req.Subject == "family" {
		known = true
	} else if s.cfg != nil {
		for _, u := range s.cfg.Users {
			if u.Name == req.Subject {
				known = true
				break
			}
		}
	}
	if !known {
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	f := familystate.Fact{
		Category: req.Category, Subject: req.Subject, Label: req.Label, Value: req.Value,
		CreatedBy: actor,
	}
	if err := s.familyState.UpsertFact(r.Context(), &f); err != nil {
		if errors.Is(err, familystate.ErrUnknownCategory) {
			http.Error(w, "unknown category", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.LogAudit(r.Context(), actor, "web", "family_state_web_upsert",
		mustJSON(map[string]any{"category": req.Category, "subject": req.Subject, "label": req.Label})); err != nil {
		// log but don't fail the request
		_ = err
	}
	writeJSON(w, http.StatusOK, f)
}

// handleFamilyStateFact dispatches DELETE on /api/family-state/facts/{id}.
func (s *Server) handleFamilyStateFact(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.familyState == nil {
		http.Error(w, "family state not configured", http.StatusServiceUnavailable)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/family-state/facts/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.familyState.DeleteFact(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.db.LogAudit(r.Context(), sess.UserName, "web", "family_state_web_delete", mustJSON(map[string]any{"id": id}))
	w.WriteHeader(http.StatusNoContent)
}

// handleFamilyStateCategories handles GET (list) and POST (create) on /api/family-state/categories.
func (s *Server) handleFamilyStateCategories(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.familyState == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	switch r.Method {
	case http.MethodGet:
		cats, err := s.familyState.ListCategories(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, cats)
	case http.MethodPost:
		var req struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			AlwaysInject bool   `json:"always_inject"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := s.familyState.UpsertCategory(r.Context(), &familystate.Category{
			Name: req.Name, Description: req.Description, AlwaysInject: req.AlwaysInject,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = s.db.LogAudit(r.Context(), sess.UserName, "web", "family_state_web_add_category",
			mustJSON(map[string]any{"name": req.Name}))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFamilyStateCategory handles DELETE on /api/family-state/categories/{name}.
func (s *Server) handleFamilyStateCategory(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.familyState == nil {
		http.Error(w, "family state not configured", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/family-state/categories/")
	if err := s.familyState.DeleteCategory(r.Context(), name); err != nil {
		switch {
		case errors.Is(err, familystate.ErrBuiltinCategory):
			http.Error(w, "cannot delete built-in category", http.StatusBadRequest)
		case errors.Is(err, familystate.ErrCategoryNotEmpty):
			http.Error(w, "category has facts; delete them first", http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	_ = s.db.LogAudit(r.Context(), sess.UserName, "web", "family_state_web_delete_category",
		mustJSON(map[string]any{"name": name}))
	w.WriteHeader(http.StatusNoContent)
}

// mustJSON / writeJSON are existing helpers — if not, add them.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// _ = context.Background // pin the import if unused above
var _ = context.Background
```

(If `writeJSON` is already in the package use the existing one; if `mustJSON` already exists drop the duplicate.)

- [ ] **Step 4: Register the routes in server.go**

Edit `internal/web/server.go`. Wherever the route mux is built, add:

```go
	mux.HandleFunc("/api/family-state/facts", s.handleFamilyStateFacts)
	mux.HandleFunc("/api/family-state/facts/", s.handleFamilyStateFact)
	mux.HandleFunc("/api/family-state/categories", s.handleFamilyStateCategories)
	mux.HandleFunc("/api/family-state/categories/", s.handleFamilyStateCategory)
	mux.HandleFunc("/dashboard/family-state", s.serveStatic("family-state.html"))
```

Add `familyState *familystate.Store` field to the `Server` struct, wire it through `NewServer`. Mirror the existing pattern (e.g., how `db` is wired).

- [ ] **Step 5: Create the dashboard template**

Create `internal/web/static/family-state.html`. Use the existing `unknown-accounts.html` or `audit.html` as a styling template. Skeleton:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Family State — FamClaw</title>
  <link rel="stylesheet" href="/static/dashboard.css">
</head>
<body>
  <header>
    <h1>Family state</h1>
    <nav>
      <a href="/dashboard">← Dashboard</a>
      <a href="/dashboard/audit?tool_name_prefix=family_">Audit log</a>
    </nav>
  </header>

  <section id="categories">
    <h2>Categories</h2>
    <ul id="cat-list"></ul>
    <button id="add-cat">+ Add category</button>
  </section>

  <section id="facts">
    <h2>Facts</h2>
    <table id="facts-table">
      <thead><tr><th>Category</th><th>Subject</th><th>Label</th><th>Value</th><th>Updated</th><th></th></tr></thead>
      <tbody></tbody>
    </table>
    <button id="add-fact">+ Add fact</button>
  </section>

  <script src="/static/family-state.js"></script>
</body>
</html>
```

And a small `family-state.js` (if the existing pages use a separate JS file) — or inline `<script>` if that's the established pattern. Keep it minimal: fetch lists, render rows, hook the add/delete buttons to the JSON endpoints. **No new framework**.

- [ ] **Step 6: Add a nav link**

Edit `internal/web/static/index.html`. Find the navigation block (where links to /dashboard/audit, /dashboard/users, etc. live) and add:

```html
<a href="/dashboard/family-state">Family state</a>
```

- [ ] **Step 7: Run tests**

Run: `go test -count=1 ./internal/web/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/web/familystate_handler.go internal/web/familystate_handler_test.go internal/web/static/family-state.html internal/web/static/index.html internal/web/server.go
git commit -m "feat(web): family-state dashboard + JSON API"
```

---

## Task 14 — Subagent BuiltinDefs filter

**Files:**
- Modify: `internal/agent/agent.go` (subagent BuiltinDefs construction)

- [ ] **Step 1: Locate the subagent BuiltinDefs filter**

In `agent.go` around line 379, the existing code filters `spawn_agent` out of subagent BuiltinDefs. Extend the filter to also exclude all mutation tools.

Replace the filter block with:

```go
	builtinDefs := make([]llm.ToolDef, 0, len(a.builtinTools))
	for _, t := range a.builtinTools {
		// Subagents are read-only. Exclude:
		// - spawn_agent (prevents runaway nesting)
		// - all family-state mutations + admin tools
		if t.Name == "builtin__spawn_agent" {
			continue
		}
		if isSubagentExcludedTool(t.Name) {
			continue
		}
		builtinDefs = append(builtinDefs, agentcoreToolToDef(t))
	}
```

Add the helper function near other helpers:

```go
// isSubagentExcludedTool reports whether the named tool must NOT be exposed
// to subagents. Subagents inherit the parent's identity but should never
// mutate shared state.
func isSubagentExcludedTool(name string) bool {
	switch name {
	case "builtin__set_family_fact",
		"builtin__delete_family_fact",
		"builtin__add_family_category",
		"builtin__delete_family_category",
		"builtin__propose_family_fact",
		"builtin__approve_request",
		"builtin__deny_request",
		"builtin__set_user_role",
		"builtin__link_account":
		return true
	}
	return false
}
```

(`agentcoreToolToDef` is the existing helper used inside the loop — if the actual name differs, just use whatever the existing code does.)

- [ ] **Step 2: Build and test**

Run: `go build ./...`
Expected: clean.

Run: `go test -count=1 ./internal/agent/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/agent.go
git commit -m "feat(subagent): exclude family-state mutations from subagent toolset"
```

---

## Task 15 — Integration tests (6 scenarios) + release notes

**Files:**
- Modify: `integration_test.go`
- Modify: `CHANGELOG.md` or release-notes-style doc (if the project keeps one)

- [ ] **Step 1: Locate the integration test file**

Run: `find . -name 'integration_test.go' -not -path './vendor/*'`
Open the file. Confirm the build tag `//go:build integration` is at the top.

- [ ] **Step 2: Add scenario 1 — parent set-via-chat → next-turn-sees-fact**

Append to `integration_test.go`:

```go
//go:build integration

// (existing tests above...)

func TestIntegration_FamilyState_ParentSetViaChat_NextTurnSeesFact(t *testing.T) {
	env := setupIntegrationEnv(t) // existing helper that brings up the test stack
	defer env.Cleanup()

	// 1. Parent sends a message that triggers set_family_fact tool call.
	// (mockLLM is configured to emit the tool_call on the first turn.)
	env.MockLLM.SetNextToolCall("builtin__set_family_fact", map[string]any{
		"category": "allergies", "subject": "teo", "label": "peanuts", "value": "severe",
	})
	env.MockLLM.SetNextResponse("Saved.")

	resp := env.SendDiscordMessage("dep", "remember Teo has a severe peanut allergy")
	if !strings.Contains(resp, "Saved") {
		t.Fatalf("first turn reply = %q, want contains 'Saved'", resp)
	}

	// 2. Verify DB row.
	facts, _ := env.FamilyState.ListFacts(env.Ctx, familystate.FilterOpts{Category: "allergies"})
	if len(facts) != 1 || facts[0].Subject != "teo" {
		t.Fatalf("facts after set = %+v", facts)
	}

	// 3. Second turn: assert the system prompt sent to mockLLM contains <family_safety>.
	env.MockLLM.SetNextResponse("Sure.")
	env.SendDiscordMessage("teo", "what should we eat?")

	sysPrompt := env.MockLLM.LastSystemPrompt()
	if !strings.Contains(sysPrompt, "<family_safety>") {
		t.Errorf("system prompt missing <family_safety>: %q", sysPrompt)
	}
	if !strings.Contains(sysPrompt, "peanuts") {
		t.Errorf("system prompt missing 'peanuts': %q", sysPrompt)
	}
}
```

(`setupIntegrationEnv`, `env.MockLLM`, `env.SendDiscordMessage`, `env.LastSystemPrompt` are whatever helpers the existing integration tests use. Reuse what's there. If a helper is missing for "set next tool call" or "capture last system prompt", add it; don't reinvent the mock LLM.)

- [ ] **Step 3: Add scenarios 2–5**

Append:

```go
func TestIntegration_FamilyState_KidProposal_ParentApproval_FactApplied(t *testing.T) {
	env := setupIntegrationEnv(t)
	defer env.Cleanup()

	// Child proposes.
	env.MockLLM.SetNextToolCall("builtin__propose_family_fact", map[string]any{
		"category": "important_dates", "subject": "teo", "label": "birthday", "value": "2012-08-04",
	})
	env.MockLLM.SetNextResponse("Sent to parents.")
	env.SendDiscordMessage("teo", "remember my birthday is August 4th 2012")

	// Verify approvals row created with kind=family_fact_proposal.
	pending, _ := env.DB.PendingApprovals(env.Ctx)
	var fp *store.Approval
	for _, a := range pending {
		if a.Category == familystate.ProposalKind {
			fp = a
		}
	}
	if fp == nil {
		t.Fatalf("no family_fact_proposal in pending approvals: %+v", pending)
	}

	// Parent approves via approve_request tool.
	env.MockLLM.SetNextToolCall("builtin__approve_request", map[string]any{
		"request_id": fp.ID,
	})
	env.MockLLM.SetNextResponse("Approved.")
	env.SendDiscordMessage("dep", "/approve "+fp.ID)

	// Verify fact landed in DB.
	facts, _ := env.FamilyState.ListFacts(env.Ctx, familystate.FilterOpts{Category: "important_dates"})
	if len(facts) != 1 || facts[0].Label != "birthday" {
		t.Errorf("facts after approve = %+v", facts)
	}
}

func TestIntegration_FamilyState_ChildBlockedFromMutation(t *testing.T) {
	env := setupIntegrationEnv(t)
	defer env.Cleanup()

	env.MockLLM.SetNextToolCall("builtin__set_family_fact", map[string]any{
		"category": "allergies", "subject": "teo", "label": "peanuts", "value": "severe",
	})
	env.MockLLM.SetNextResponse("(should not reach here on the same turn)")
	resp := env.SendDiscordMessage("teo", "save my allergy")

	// Tool result should be the OPA denial reason; DB must not have the row.
	if !strings.Contains(strings.ToLower(resp), "restricted") && !strings.Contains(strings.ToLower(resp), "not authorized") {
		t.Errorf("child set_family_fact reply = %q, want a denial message", resp)
	}
	facts, _ := env.FamilyState.ListFacts(env.Ctx, familystate.FilterOpts{Category: "allergies"})
	if len(facts) != 0 {
		t.Errorf("child should not be able to write a fact; got %d rows", len(facts))
	}
}

func TestIntegration_FamilyState_ParentAutoApplyGoesThroughOPA(t *testing.T) {
	env := setupIntegrationEnv(t)
	defer env.Cleanup()

	env.PolicyTracer.Reset() // assume the integration env wraps the OPA evaluator to record decisions

	env.MockLLM.SetNextToolCall("builtin__propose_family_fact", map[string]any{
		"category": "pets", "subject": "family", "label": "Stella", "value": "cat",
	})
	env.MockLLM.SetNextResponse("Added.")
	env.SendDiscordMessage("dep", "save that we have a cat named Stella")

	// Assert OPA was invoked with the synthetic name.
	if !env.PolicyTracer.SawTool("family_fact_proposal_auto_apply") {
		t.Errorf("policy was not consulted for auto-apply; tracer saw: %+v", env.PolicyTracer.Tools())
	}
	facts, _ := env.FamilyState.ListFacts(env.Ctx, familystate.FilterOpts{Category: "pets"})
	if len(facts) != 1 {
		t.Errorf("auto-apply did not write; got %d facts", len(facts))
	}
}

func TestIntegration_FamilyState_WebCRUDToNextTurnInPrompt(t *testing.T) {
	env := setupIntegrationEnv(t)
	defer env.Cleanup()

	// POST via the dashboard endpoint as a parent session.
	resp := env.WebPOST("/api/family-state/facts", `{"category":"dietary_restrictions","subject":"family","label":"kosher","value":"kosher household"}`, env.ParentCookie())
	if resp.StatusCode != 200 {
		t.Fatalf("web POST = %d", resp.StatusCode)
	}

	// Next chat turn — assert <family_safety> contains kosher.
	env.MockLLM.SetNextResponse("Got it.")
	env.SendDiscordMessage("teo", "what's for dinner?")
	sys := env.MockLLM.LastSystemPrompt()
	if !strings.Contains(sys, "kosher") || !strings.Contains(sys, "<family_safety>") {
		t.Errorf("web write did not reach next-turn prompt: %q", sys)
	}
}
```

- [ ] **Step 4: Add scenario 6 — failure mode**

Append:

```go
func TestIntegration_FamilyState_SnapshotErrorYieldsUnavailableNotice(t *testing.T) {
	env := setupIntegrationEnv(t)
	defer env.Cleanup()

	// Force a snapshot error. Easiest path: close the DB after env startup
	// and re-open with a broken handle. Or use a test hook on the Store.
	// (Implementer: pick whichever hook the env helper exposes; if none,
	// add a SetForcedSnapshotError(err) hook to familystate.Store specifically
	// for integration tests, gated behind a //go:build integration tag.)
	env.FamilyState.SetForcedSnapshotError(fmt.Errorf("forced for test"))
	t.Cleanup(func() { env.FamilyState.SetForcedSnapshotError(nil) })

	env.MockLLM.SetNextResponse("ok")
	env.SendDiscordMessage("teo", "anything")

	sys := env.MockLLM.LastSystemPrompt()
	if !strings.Contains(sys, "<family_safety>safety context temporarily unavailable") {
		t.Errorf("unavailable notice missing from prompt: %q", sys)
	}
}
```

If `SetForcedSnapshotError` doesn't exist, add it to `internal/familystate/store.go` with a build tag:

```go
//go:build integration

package familystate

// SetForcedSnapshotError is a test-only hook to force AlwaysInjectedSnapshot
// to return the given error. Build-tag gated so production builds do not
// include it.
func (s *Store) SetForcedSnapshotError(err error) {
	// implementation: a package-level var checked at the top of AlwaysInjectedSnapshot
}
```

Or simpler: in the integration env, swap the `Store` for a `failingStore` that implements the same minimal interface and returns the forced error from `AlwaysInjectedSnapshot`.

- [ ] **Step 5: Run integration tests**

Run: `go test -tags integration ./... -v`
Expected: all PASS.

- [ ] **Step 6: Update CHANGELOG.md (or equivalent)**

If the project has a `CHANGELOG.md`, prepend a new entry:

```markdown
## Unreleased

### Added — Phase 3.3: Family state

- Shared, parent-managed family memory with five built-in tools:
  `get_family_state` (read, all roles), `set_family_fact` /
  `delete_family_fact` / `add_family_category` / `delete_family_category`
  (parent-only), and `propose_family_fact` (kid-proposable with parent
  approval via the existing approvals flow).
- Allergies and dietary restrictions are wrapped in `<family_safety>...</family_safety>`
  and injected into every system prompt; pets, important_dates, and any
  custom categories are read on demand via `get_family_state`.
- Web dashboard page at `/dashboard/family-state` for parent edits.
- Subject↔config drift detection: orphaned rows (subjects renamed out of
  config.yaml) are excluded from the always-injected snapshot and surfaced
  in the dashboard.
- On snapshot read failure, the system prompt receives a
  `<family_safety>safety context temporarily unavailable</family_safety>`
  notice so the model knows it is operating without safety context.
- New OPA tool-policy entries gate the four mutation tools and a synthetic
  `family_fact_proposal_auto_apply` rule that closes a Go-only authorization
  gap on the parent auto-apply branch.
- `recurrence TEXT NULL` column added to `family_facts` for Phase 3.1
  reminders to consume without a follow-up migration.

### Notes / known limitations

- The kid-proposal notification copy uses the existing content-approval
  format, which reads awkwardly for fact proposals (TODO: v2).
```

- [ ] **Step 7: Final build sanity**

Run: `make cross`
Expected: all 6 cross-compile targets succeed cleanly with `CGO_ENABLED=0`.

- [ ] **Step 8: Manual smoke per spec §12**

Bring up the bot on a fresh DB (`rm ~/.famclaw/famclaw.db` and restart). Then:

1. Open `/dashboard/family-state` as a parent. Add a `pets` fact: `subject=family, label=Stella, value=cat, age 5`. Confirm the row appears.
2. From Discord, ask "what's our cat's name?". The bot should call `get_family_state` and answer "Stella".
3. Add an `allergies` fact for `teo`. Restart the bot (or wait — snapshot reloads per turn). Ask "what should we make for dinner?" as `teo`. The reply should reflect awareness of the peanut allergy.
4. Rename `teo` → `theodore` in `config.yaml`. Restart. Ask anything. Verify (a) the `teo` allergy row is excluded from the snapshot (check logs for `slog.Warn`), (b) the dashboard "orphaned facts" UI shows the row.

- [ ] **Step 9: Commit**

```bash
git add integration_test.go CHANGELOG.md internal/familystate/*.go
git commit -m "test(integration): 6 family-state scenarios + release notes"
```

---

## Final wrap

- [ ] **Run full test suite**

```bash
go test -count=1 ./...
go test -tags integration -count=1 ./...
opa test internal/policy/policies/family/ internal/policy/policies/data/ -v
make cross
```

All must pass.

- [ ] **Open PR**

```bash
git push -u origin spec/family-state
gh pr create --title "feat(phase3.3): family state — shared memory with safety injection" --body "..."
```

PR body should reference the spec and council debate transcripts.

---

## Self-review notes

- **Coverage:** Every section of the spec maps to a task — schema (T2), familystate package (T1, T3-6), prompt integration (T8), agent wiring (T9), tools (T10-12), web (T13), subagent filter (T14), tests (T15).
- **R3 council fixes traced through:**
  - Q5 2-branch fail-stance → Task 9 step 3 (UnavailableSnapshot wiring) + Task 15 step 4 (failure-mode integration test).
  - OPA hole → Task 7 (synthetic admin_tools entry) + Task 12 step 3 (parent auto-apply OPA call).
  - Recurrence column → Task 2 (schema) + ready for Phase 3.1 reads.
  - `<family_safety>` tag wrapper → Task 5 (Snapshot.Render) + Task 8 step 4 (snapshot test pins format).
  - Subject↔config drift → Task 5 (knownSubjects filter + slog.Warn) + Task 9 step 4 (knownSubjects helper).
  - Expanded integration tests → Task 15 (6 scenarios).
- **TODO marker for notify copy:** Task 12 step 3 has the `// TODO(famclaw/issue-XXX)` comment as called out in the spec §11.

---
