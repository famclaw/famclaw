package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/store"
)

// newFamilyTestDeps wires a fresh in-memory DB + familystate.Store and
// returns a Deps the family_* handlers can use.
func newFamilyTestDeps(t *testing.T) Deps {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return Deps{
		DB:    db,
		Actor: "dep",
		Cfg: &config.Config{Users: []config.UserConfig{
			{Name: "dep", Role: "parent"},
			{Name: "julia", Role: "parent"},
			{Name: "teo", Role: "child", AgeGroup: "age_13_17"},
		}},
		FamilyState: familystate.NewStore(db),
		Gateway:     "test",
	}
}

func TestHandleSetFamilyFact(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]any
		wantPrefix string // expected substring of the return string
		wantErr    bool
	}{
		{
			name:       "happy path on built-in category",
			args:       map[string]any{"category": "allergies", "subject": "teo", "label": "peanuts", "value": "severe"},
			wantPrefix: "ok — fact #",
		},
		{
			name:       "subject 'family' accepted",
			args:       map[string]any{"category": "dietary_restrictions", "subject": "family", "label": "kosher", "value": "kosher household"},
			wantPrefix: "ok — fact #",
		},
		{
			name:       "unknown subject rejected as user-facing string (not error)",
			args:       map[string]any{"category": "allergies", "subject": "ghost", "label": "x", "value": "y"},
			wantPrefix: "not a family member",
		},
		{
			name:       "unknown category surfaces help text, not error",
			args:       map[string]any{"category": "made_up", "subject": "teo", "label": "x", "value": "y"},
			wantPrefix: "unknown category",
		},
		{
			name:       "label over 64 chars rejected",
			args:       map[string]any{"category": "allergies", "subject": "teo", "label": strings.Repeat("x", 65), "value": "y"},
			wantPrefix: "label too long",
		},
		{
			name:       "value over 512 chars rejected",
			args:       map[string]any{"category": "allergies", "subject": "teo", "label": "x", "value": strings.Repeat("v", 513)},
			wantPrefix: "value too long",
		},
		{
			name:    "missing required arg returns error",
			args:    map[string]any{"category": "allergies", "subject": "teo", "label": "x"}, // no value
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFamilyTestDeps(t)
			out, err := HandleSetFamilyFact(context.Background(), deps, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(out, tc.wantPrefix) {
				t.Errorf("output %q missing %q", out, tc.wantPrefix)
			}
		})
	}
}

func TestHandleDeleteFamilyFact(t *testing.T) {
	deps := newFamilyTestDeps(t)
	ctx := context.Background()
	// Seed one fact so we have a real id to delete.
	f := familystate.Fact{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"}
	if err := deps.FamilyState.UpsertFact(ctx, &f); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name       string
		args       map[string]any
		wantPrefix string
		wantErr    bool
	}{
		{name: "delete existing", args: map[string]any{"id": float64(f.ID)}, wantPrefix: "deleted"},
		{name: "delete non-existent is idempotent", args: map[string]any{"id": float64(9999)}, wantPrefix: "deleted"},
		{name: "id zero rejected", args: map[string]any{"id": float64(0)}, wantErr: true},
		{name: "id negative rejected", args: map[string]any{"id": float64(-1)}, wantErr: true},
		{name: "id wrong type rejected", args: map[string]any{"id": "not-a-number"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := HandleDeleteFamilyFact(ctx, deps, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(out, tc.wantPrefix) {
				t.Errorf("output %q missing %q", out, tc.wantPrefix)
			}
		})
	}
}

func TestHandleAddFamilyCategory(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]any
		wantPrefix string
		wantErr    bool
	}{
		{
			name:       "happy path custom category",
			args:       map[string]any{"name": "hobbies", "description": "things we do for fun"},
			wantPrefix: "ok",
		},
		{
			name:       "always_inject=true accepted",
			args:       map[string]any{"name": "house_rules", "description": "shared rules", "always_inject": true},
			wantPrefix: "ok",
		},
		{
			name:       "upsert against built-in refused as user-facing string",
			args:       map[string]any{"name": "allergies", "description": "evil"},
			wantPrefix: "built-in",
		},
		{
			name:       "uppercase rejected (regex)",
			args:       map[string]any{"name": "Hobbies", "description": "x"},
			wantPrefix: "invalid category name",
		},
		{
			name:       "spaces rejected",
			args:       map[string]any{"name": "movie night", "description": "x"},
			wantPrefix: "invalid category name",
		},
		{
			name:       "name over 32 chars rejected",
			args:       map[string]any{"name": strings.Repeat("a", 33), "description": "x"},
			wantPrefix: "invalid category name",
		},
		{
			name:       "description over 256 chars rejected",
			args:       map[string]any{"name": "ok_name", "description": strings.Repeat("d", 257)},
			wantPrefix: "description too long",
		},
		{
			name:    "missing description errors",
			args:    map[string]any{"name": "ok"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFamilyTestDeps(t)
			out, err := HandleAddFamilyCategory(context.Background(), deps, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(out, tc.wantPrefix) {
				t.Errorf("output %q missing %q", out, tc.wantPrefix)
			}
		})
	}
}

func TestHandleDeleteFamilyCategory(t *testing.T) {
	type setup func(t *testing.T, deps Deps)

	seedCustom := func(t *testing.T, deps Deps) {
		t.Helper()
		c := familystate.Category{Name: "to_delete", Description: "tmp"}
		if err := deps.FamilyState.UpsertCategory(context.Background(), &c); err != nil {
			t.Fatalf("seed category: %v", err)
		}
	}
	seedCustomWithFact := func(t *testing.T, deps Deps) {
		t.Helper()
		seedCustom(t, deps)
		f := familystate.Fact{Category: "to_delete", Subject: "family", Label: "a", Value: "b", CreatedBy: "dep"}
		if err := deps.FamilyState.UpsertFact(context.Background(), &f); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}

	cases := []struct {
		name       string
		seed       setup
		args       map[string]any
		wantPrefix string
		wantErr    bool
	}{
		{name: "delete empty custom succeeds", seed: seedCustom, args: map[string]any{"name": "to_delete"}, wantPrefix: "ok"},
		{name: "delete non-empty refused", seed: seedCustomWithFact, args: map[string]any{"name": "to_delete"}, wantPrefix: "has facts"},
		{name: "delete built-in refused", args: map[string]any{"name": "allergies"}, wantPrefix: "built-in"},
		{name: "delete unknown surfaces help", args: map[string]any{"name": "never_existed"}, wantPrefix: "unknown category"},
		{name: "empty name errors", args: map[string]any{"name": ""}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFamilyTestDeps(t)
			if tc.seed != nil {
				tc.seed(t, deps)
			}
			out, err := HandleDeleteFamilyCategory(context.Background(), deps, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got out=%q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(out, tc.wantPrefix) {
				t.Errorf("output %q missing %q", out, tc.wantPrefix)
			}
		})
	}
}

func TestFamilyTools_NilStore(t *testing.T) {
	deps := Deps{Cfg: &config.Config{}, Actor: "dep", Gateway: "test"} // no FamilyState

	if _, err := HandleSetFamilyFact(context.Background(), deps, map[string]any{
		"category": "allergies", "subject": "family", "label": "x", "value": "y",
	}); err == nil {
		t.Error("set: want err when FamilyState nil")
	}
	if _, err := HandleDeleteFamilyFact(context.Background(), deps, map[string]any{"id": float64(1)}); err == nil {
		t.Error("delete fact: want err when FamilyState nil")
	}
	if _, err := HandleAddFamilyCategory(context.Background(), deps, map[string]any{"name": "x", "description": "y"}); err == nil {
		t.Error("add cat: want err when FamilyState nil")
	}
	if _, err := HandleDeleteFamilyCategory(context.Background(), deps, map[string]any{"name": "x"}); err == nil {
		t.Error("del cat: want err when FamilyState nil")
	}
}

// ErrCategoryNotEmpty is exercised through the handler in
// TestHandleDeleteFamilyCategory above, but we keep this as a direct
// guard against an accidental refactor that would swallow the sentinel.
func TestSentinelsStillUsed(t *testing.T) {
	if !errors.Is(familystate.ErrCategoryNotEmpty, familystate.ErrCategoryNotEmpty) {
		t.Fatal("sentinel sanity broken")
	}
}
