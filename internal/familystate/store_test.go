package familystate

import (
	"context"
	"errors"
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
		Category:  "does_not_exist",
		Subject:   "teo",
		Label:     "x",
		Value:     "y",
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
