package familystate

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestListCategories_SeededBuiltins is a single-purpose test — the seed
// is a migration-time fact, not a parameterizable scenario.
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

// TestStore_CategoryLifecycle exercises Upsert + Delete across every
// combination that matters: new custom, update custom, refusal of
// builtin upsert, refusal of builtin delete, refusal of non-empty
// delete, success of empty-custom delete, unknown-category errors.
func TestStore_CategoryLifecycle(t *testing.T) {
	type op struct {
		kind   string // "upsert" | "delete"
		cat    Category
		name   string // for delete
		wantOK bool
		wantIs error
	}

	cases := []struct {
		name string
		// seedFacts are upserted under newly-created custom categories
		// before the ops run, to set up non-empty-delete scenarios.
		seedFacts []Fact
		ops       []op
	}{
		{
			name: "upsert new custom category",
			ops: []op{
				{kind: "upsert", cat: Category{Name: "user_preferences", Description: "things they like"}, wantOK: true},
			},
		},
		{
			name: "update existing custom category description",
			ops: []op{
				{kind: "upsert", cat: Category{Name: "hobbies", Description: "initial"}, wantOK: true},
				{kind: "upsert", cat: Category{Name: "hobbies", Description: "edited"}, wantOK: true},
			},
		},
		{
			name: "upsert refuses to overwrite builtin (always_inject safety contract)",
			ops: []op{
				// caller tries to flip allergies.always_inject = false
				{kind: "upsert", cat: Category{Name: "allergies", Description: "evil", AlwaysInject: false}, wantIs: ErrBuiltinCategory},
			},
		},
		{
			name: "delete custom category with no facts succeeds",
			ops: []op{
				{kind: "upsert", cat: Category{Name: "to_delete", Description: "tmp"}, wantOK: true},
				{kind: "delete", name: "to_delete", wantOK: true},
			},
		},
		{
			name: "delete builtin refused",
			ops: []op{
				{kind: "delete", name: "allergies", wantIs: ErrBuiltinCategory},
			},
		},
		{
			name:      "delete non-empty refused",
			seedFacts: []Fact{{Category: "hold_facts", Subject: "family", Label: "x", Value: "y", CreatedBy: "dep"}},
			ops: []op{
				{kind: "upsert", cat: Category{Name: "hold_facts", Description: "holds a fact"}, wantOK: true},
				// seed fact is applied between these by the harness below
				{kind: "delete", name: "hold_facts", wantIs: ErrCategoryNotEmpty},
			},
		},
		{
			name: "delete unknown category",
			ops: []op{
				{kind: "delete", name: "never_existed", wantIs: ErrUnknownCategory},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			ctx := context.Background()

			// Resolve op ordering: any "upsert" must run before the
			// seed facts that reference it; we honor declaration order.
			seeded := false
			for _, o := range tc.ops {
				var err error
				switch o.kind {
				case "upsert":
					c := o.cat
					err = s.UpsertCategory(ctx, &c)
					// after first successful upsert, apply seed facts if any
					if err == nil && !seeded && len(tc.seedFacts) > 0 {
						for _, f := range tc.seedFacts {
							f := f
							if ferr := s.UpsertFact(ctx, &f); ferr != nil {
								t.Fatalf("seed fact: %v", ferr)
							}
						}
						seeded = true
					}
				case "delete":
					err = s.DeleteCategory(ctx, o.name)
				default:
					t.Fatalf("unknown op kind %q", o.kind)
				}
				if o.wantOK && err != nil {
					t.Fatalf("op %+v: want ok, got %v", o, err)
				}
				if o.wantIs != nil && !errors.Is(err, o.wantIs) {
					t.Fatalf("op %+v: want %v, got %v", o, o.wantIs, err)
				}
			}
		})
	}
}

// TestStore_FactRoundTrip covers UpsertFact insert/update plus
// the contract that ListFacts returns the persisted row exactly.
func TestStore_FactRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		// initial is upserted first; under is upserted second (may
		// reuse the same UNIQUE key to trigger the conflict path).
		initial *Fact
		update  *Fact
		// after update, want these field values on the returned Fact.
		wantValue     string
		wantCreatedBy string // must equal initial.CreatedBy on conflict
	}{
		{
			name:          "fresh insert",
			initial:       &Fact{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
			wantValue:     "severe",
			wantCreatedBy: "dep",
		},
		{
			name:          "update preserves created_by from original insert",
			initial:       &Fact{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "mild", CreatedBy: "dep"},
			update:        &Fact{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe — EpiPen in Mom's purse", CreatedBy: "julia"},
			wantValue:     "severe — EpiPen in Mom's purse",
			wantCreatedBy: "dep",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			ctx := context.Background()

			if err := s.UpsertFact(ctx, tc.initial); err != nil {
				t.Fatalf("initial upsert: %v", err)
			}
			origCreatedAt := tc.initial.CreatedAt
			if tc.initial.ID == 0 {
				t.Fatal("initial upsert did not populate ID")
			}

			final := tc.initial
			if tc.update != nil {
				// Ensure measurable time advances so updated_at can
				// be observably ≥ original.
				time.Sleep(1100 * time.Millisecond)
				if err := s.UpsertFact(ctx, tc.update); err != nil {
					t.Fatalf("update upsert: %v", err)
				}
				if tc.update.ID != tc.initial.ID {
					t.Errorf("update changed ID: %d → %d", tc.initial.ID, tc.update.ID)
				}
				if !tc.update.CreatedAt.Equal(origCreatedAt) {
					t.Errorf("update changed CreatedAt: %v → %v", origCreatedAt, tc.update.CreatedAt)
				}
				if !tc.update.UpdatedAt.After(origCreatedAt) {
					t.Errorf("update did not advance UpdatedAt: %v not > %v", tc.update.UpdatedAt, origCreatedAt)
				}
				final = tc.update
			}

			if final.Value != tc.wantValue {
				t.Errorf("Value = %q, want %q", final.Value, tc.wantValue)
			}
			if final.CreatedBy != tc.wantCreatedBy {
				t.Errorf("CreatedBy = %q, want %q", final.CreatedBy, tc.wantCreatedBy)
			}

			got, err := s.ListFacts(ctx, FilterOpts{Category: final.Category})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d facts, want 1", len(got))
			}
			if got[0].Value != tc.wantValue || got[0].CreatedBy != tc.wantCreatedBy {
				t.Errorf("persisted = {%q, %q}, want {%q, %q}", got[0].Value, got[0].CreatedBy, tc.wantValue, tc.wantCreatedBy)
			}
		})
	}
}

// TestStore_FactErrors covers all error paths through UpsertFact.
func TestStore_FactErrors(t *testing.T) {
	cases := []struct {
		name string
		fact Fact
		want error
	}{
		{
			name: "unknown category",
			fact: Fact{Category: "does_not_exist", Subject: "teo", Label: "x", Value: "y", CreatedBy: "dep"},
			want: ErrUnknownCategory,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			f := tc.fact
			err := s.UpsertFact(context.Background(), &f)
			if !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}
		})
	}
}
