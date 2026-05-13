package familystate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/store"
)

// newTestStore opens an in-memory SQLite DB with the migration applied
// (including seeding the 4 builtin categories) and returns a wired
// familystate.Store plus the underlying *store.DB for direct SQL access.
func newTestStore(t *testing.T) (*familystate.Store, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return familystate.NewStore(db), db
}

// ── ListCategories ────────────────────────────────────────────────────────────

// TestListCategories_Seeds verifies that the four builtin categories are
// seeded by migrate() and returned by ListCategories.
func TestListCategories_Seeds(t *testing.T) {
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

// ── UpsertCategory ────────────────────────────────────────────────────────────

// TestUpsertCategory_NewCategory verifies that a new non-builtin category
// is created and returned by ListCategories.
func TestUpsertCategory_NewCategory(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertCategory(ctx, &familystate.Category{Name: "hobbies", Description: "Family hobbies"}); err != nil {
		t.Fatalf("upsert category: %v", err)
	}

	cats, err := s.ListCategories(ctx)
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}

	found := false
	for _, c := range cats {
		if c.Name == "hobbies" {
			found = true
			if c.IsBuiltin {
				t.Error("new category should not be builtin")
			}
			if c.Description != "Family hobbies" {
				t.Errorf("description = %q, want %q", c.Description, "Family hobbies")
			}
		}
	}
	if !found {
		t.Error("new category 'hobbies' not found in ListCategories")
	}
}

// TestUpsertCategory_BuiltinRejected verifies that trying to upsert over a
// builtin category returns ErrBuiltinCategory.
func TestUpsertCategory_BuiltinRejected(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.UpsertCategory(context.Background(), &familystate.Category{
		Name:        "allergies",
		Description: "hijacked description",
	})
	if !errors.Is(err, familystate.ErrBuiltinCategory) {
		t.Errorf("want ErrBuiltinCategory, got %v", err)
	}
}

// ── DeleteCategory ────────────────────────────────────────────────────────────

// TestDeleteCategory_OK verifies that an empty custom category can be deleted.
func TestDeleteCategory_OK(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertCategory(ctx, &familystate.Category{Name: "custom2", Description: "test"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteCategory(ctx, "custom2"); err != nil {
		t.Errorf("delete: %v", err)
	}

	// Verify gone from ListCategories.
	cats, err := s.ListCategories(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range cats {
		if c.Name == "custom2" {
			t.Error("deleted category 'custom2' still appears in ListCategories")
		}
	}
}

// TestDeleteCategory_BuiltinRejected verifies that deleting a builtin returns
// ErrBuiltinCategory.
func TestDeleteCategory_BuiltinRejected(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.DeleteCategory(context.Background(), "allergies")
	if !errors.Is(err, familystate.ErrBuiltinCategory) {
		t.Errorf("want ErrBuiltinCategory, got %v", err)
	}
}

// TestDeleteCategory_NonEmptyRejected verifies that deleting a category that
// still has facts returns ErrCategoryNotEmpty.
func TestDeleteCategory_NonEmptyRejected(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertCategory(ctx, &familystate.Category{Name: "custom1", Description: "test"}); err != nil {
		t.Fatalf("upsert category: %v", err)
	}
	if err := s.UpsertFact(ctx, &familystate.Fact{
		Category: "custom1", Subject: "family", Label: "a", Value: "b", CreatedBy: "dep",
	}); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}
	err := s.DeleteCategory(ctx, "custom1")
	if !errors.Is(err, familystate.ErrCategoryNotEmpty) {
		t.Errorf("want ErrCategoryNotEmpty, got %v", err)
	}
}

// ── UpsertFact ────────────────────────────────────────────────────────────────

// TestUpsertFact_OK verifies that a fact is inserted and the ID is populated.
func TestUpsertFact_OK(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	f := &familystate.Fact{
		Category:  "allergies",
		Subject:   "teo",
		Label:     "peanuts",
		Value:     "severe — EpiPen in Mom's purse",
		CreatedBy: "dep",
	}
	if err := s.UpsertFact(ctx, f); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("upsert did not populate ID")
	}
}

// TestUpsertFact_UnknownCategory verifies that inserting a fact with a
// nonexistent category returns ErrUnknownCategory.
func TestUpsertFact_UnknownCategory(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.UpsertFact(context.Background(), &familystate.Fact{
		Category:  "does_not_exist",
		Subject:   "teo",
		Label:     "x",
		Value:     "y",
		CreatedBy: "dep",
	})
	if !errors.Is(err, familystate.ErrUnknownCategory) {
		t.Errorf("want ErrUnknownCategory, got %v", err)
	}
}

// TestUpsertFact_UpdateBranch verifies that upserting the same
// (category, subject, label) key updates the value.
func TestUpsertFact_UpdateBranch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	f := &familystate.Fact{
		Category:  "allergies",
		Subject:   "teo",
		Label:     "peanuts",
		Value:     "mild",
		CreatedBy: "dep",
	}
	if err := s.UpsertFact(ctx, f); err != nil {
		t.Fatalf("insert: %v", err)
	}
	firstID := f.ID

	// Upsert same key with new value.
	f2 := &familystate.Fact{
		Category:  "allergies",
		Subject:   "teo",
		Label:     "peanuts",
		Value:     "severe — EpiPen required",
		CreatedBy: "dep",
	}
	if err := s.UpsertFact(ctx, f2); err != nil {
		t.Fatalf("update: %v", err)
	}
	if f2.ID == 0 {
		t.Fatal("update branch did not populate ID")
	}
	if f2.ID != firstID {
		t.Errorf("ID changed on update: got %d, want %d", f2.ID, firstID)
	}

	// Verify via ListFacts.
	facts, err := s.ListFacts(ctx, familystate.FilterOpts{Category: "allergies", Subject: "teo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("got %d facts, want 1", len(facts))
	}
	if facts[0].Value != "severe — EpiPen required" {
		t.Errorf("value not updated: got %q", facts[0].Value)
	}
}

// ── ListFacts ─────────────────────────────────────────────────────────────────

// TestListFacts_FilterBySubject verifies that FilterOpts.Subject narrows results.
func TestListFacts_FilterBySubject(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	facts := []familystate.Fact{
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
		{Category: "allergies", Subject: "julia", Label: "shellfish", Value: "mild", CreatedBy: "dep"},
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat", CreatedBy: "dep"},
	}
	for i := range facts {
		if err := s.UpsertFact(ctx, &facts[i]); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
	}

	got, err := s.ListFacts(ctx, familystate.FilterOpts{Subject: "teo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d facts, want 1", len(got))
	}
	if got[0].Subject != "teo" {
		t.Errorf("wrong subject: %q", got[0].Subject)
	}
}

// ── DeleteFact ────────────────────────────────────────────────────────────────

// TestDeleteFact_OK verifies that a fact is deleted and no longer visible in
// ListFacts.
func TestDeleteFact_OK(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	f := &familystate.Fact{
		Category:  "pets",
		Subject:   "family",
		Label:     "Stella",
		Value:     "cat",
		CreatedBy: "dep",
	}
	if err := s.UpsertFact(ctx, f); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.DeleteFact(ctx, f.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Verify gone.
	got, err := s.ListFacts(ctx, familystate.FilterOpts{Category: "pets"})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d facts after delete, want 0", len(got))
	}
}
