package convmem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)

func newSearchDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSearchDefinition(t *testing.T) {
	d := SearchDefinition()
	if d.Name != "builtin__search_conversations" {
		t.Fatalf("name = %q", d.Name)
	}
	if d.Source != "builtin" {
		t.Fatalf("source = %q", d.Source)
	}
	// Nit F4: roles are explicitly enumerated, not left empty.
	wantRoles := []string{"parent", "child", "age_8_12", "age_13_17", "under_8"}
	if len(d.Roles) != len(wantRoles) {
		t.Fatalf("roles = %v, want %v", d.Roles, wantRoles)
	}
	for i, r := range wantRoles {
		if d.Roles[i] != r {
			t.Fatalf("roles = %v, want %v", d.Roles, wantRoles)
		}
	}
	for _, r := range wantRoles {
		if !d.AllowedForRole(r) {
			t.Errorf("AllowedForRole(%q) = false, want true", r)
		}
	}
	schema, _ := d.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"query", "from_date", "to_date", "max_results"} {
		if _, ok := schema[k]; !ok {
			t.Errorf("input schema missing property %q", k)
		}
	}
}

func TestHandleSearch_ScopingAndFormatting(t *testing.T) {
	db := newSearchDB(t)
	ctx := context.Background()

	if err := db.SaveMessage("conv-alice", "alice", "user", "planning lunch at the ramen place", "", "", "web"); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := db.SaveMessage("conv-bob", "bob", "assistant", "bob noted the sushi secret spot", "", "", "web"); err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	out, err := HandleSearch(ctx, db, "alice", "lunch", "", "", 0)
	if err != nil {
		t.Fatalf("HandleSearch: %v", err)
	}
	for _, want := range []string{"1 matches", "lunch", "conv conv-alice", "user", "2026"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sushi") {
		t.Errorf("another user's message leaked:\n%s", out)
	}
}

func TestHandleSearch_EmptyResult(t *testing.T) {
	db := newSearchDB(t)
	ctx := context.Background()
	if err := db.SaveMessage("conv-x", "alice", "user", "nothing searchable here", "", "", "web"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := HandleSearch(ctx, db, "alice", "zebra", "", "", 10)
	if err != nil {
		t.Fatalf("HandleSearch: %v", err)
	}
	if out != "No matches." {
		t.Fatalf("expected literal 'No matches.', got %q", out)
	}
}

func TestHandleSearch_RejectsBlankQuery(t *testing.T) {
	db := newSearchDB(t)
	if _, err := HandleSearch(context.Background(), db, "alice", "   ", "", "", 10); err == nil {
		t.Fatalf("expected error for blank query")
	}
}

func TestHandleSearch_MaxResultsClamp(t *testing.T) {
	db := newSearchDB(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		if err := db.SaveMessage("conv-cap", "alice", "user",
			"widget observation "+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)), "", "", "web"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out, err := HandleSearch(ctx, db, "alice", "widget", "", "", 1000)
	if err != nil {
		t.Fatalf("HandleSearch: %v", err)
	}
	n := strings.Count(out, "\n- [conv ")
	if n > 25 {
		t.Fatalf("handler returned %d lines, exceeds the 25 hard cap", n)
	}
	if n != 25 {
		t.Fatalf("expected 25 lines, got %d", n)
	}
}
