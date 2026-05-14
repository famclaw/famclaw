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
