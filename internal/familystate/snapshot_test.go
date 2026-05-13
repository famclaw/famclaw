package familystate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/familystate"
)

// ── IsEmpty ───────────────────────────────────────────────────────────────────

// TestSnapshot_IsEmpty verifies the three cases: nil entries (empty), non-empty,
// and the unavailable sentinel (not empty).
func TestSnapshot_IsEmpty(t *testing.T) {
	// Zero-value Snapshot (no entries, not unavailable) → true.
	empty := &familystate.Snapshot{}
	if !empty.IsEmpty() {
		t.Error("zero-value Snapshot should be IsEmpty")
	}

	// Snapshot with one fact → false.
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertFact(ctx, &familystate.Fact{
		Category:  "allergies",
		Subject:   "teo",
		Label:     "peanuts",
		Value:     "severe",
		CreatedBy: "dep",
	}); err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	snap, err := s.AlwaysInjectedSnapshot(ctx, map[string]bool{"teo": true})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.IsEmpty() {
		t.Error("snapshot with fact should NOT be IsEmpty")
	}

	// UnavailableSnapshot → false.
	if familystate.UnavailableSnapshot().IsEmpty() {
		t.Error("UnavailableSnapshot should NOT be IsEmpty")
	}
}

// ── Render: unavailable sentinel ─────────────────────────────────────────────

// TestSnapshot_Render_Unavailable verifies the exact output of the unavailable
// sentinel.
func TestSnapshot_Render_Unavailable(t *testing.T) {
	out := familystate.UnavailableSnapshot().Render()
	const want = "<family_safety>\nsafety context temporarily unavailable — operating with reduced family context\n</family_safety>"
	if out != want {
		t.Errorf("UnavailableSnapshot.Render() =\n%q\nwant:\n%q", out, want)
	}
	// Must be wrapped in tags.
	if !strings.HasPrefix(out, "<family_safety>") {
		t.Error("must start with <family_safety>")
	}
	if !strings.HasSuffix(out, "</family_safety>") {
		t.Error("must end with </family_safety>")
	}
}

// ── Render: with facts ────────────────────────────────────────────────────────

// TestSnapshot_Render_WithFacts verifies that Render is deterministic with
// "family" subject first and other subjects alphabetically, and that categories
// are rendered alphabetically.
func TestSnapshot_Render_WithFacts(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	// Seed two always-inject categories with multiple subjects.
	facts := []familystate.Fact{
		{Category: "dietary_restrictions", Subject: "teo", Label: "vegetarian", Value: "no meat", CreatedBy: "dep"},
		{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher household", CreatedBy: "dep"},
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
	}
	for i := range facts {
		if err := s.UpsertFact(ctx, &facts[i]); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
	}

	known := map[string]bool{"teo": true, "family": true}
	snap, err := s.AlwaysInjectedSnapshot(ctx, known)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	out := snap.Render()

	// Must be wrapped in tags.
	if !strings.HasPrefix(out, "<family_safety>\n") {
		t.Errorf("Render must start with <family_safety>\\n; got %q", out[:min(40, len(out))])
	}
	if !strings.HasSuffix(out, "\n</family_safety>") {
		t.Errorf("Render must end with \\n</family_safety>; tail: %q", out[max(0, len(out)-40):])
	}

	// "allergies" category should appear before "dietary_restrictions" (alpha).
	allergiesIdx := strings.Index(out, "Allergies")
	dietIdx := strings.Index(out, "Dietary")
	if allergiesIdx == -1 || dietIdx == -1 {
		t.Fatalf("expected both Allergies and Dietary in output; got:\n%s", out)
	}
	if allergiesIdx > dietIdx {
		t.Errorf("allergies (%d) should come before dietary_restrictions (%d); output:\n%s",
			allergiesIdx, dietIdx, out)
	}

	// Within the dietary_restrictions line, "Family" should come before "Teo".
	// Find the dietary line and check order within it.
	dietStart := strings.Index(out, "Dietary")
	if dietStart == -1 {
		t.Fatalf("Dietary section not found in output:\n%s", out)
	}
	dietLine := out[dietStart:]
	// Find next newline to bound to just this line.
	if nl := strings.Index(dietLine, "\n"); nl != -1 {
		dietLine = dietLine[:nl]
	}
	familyInDiet := strings.Index(dietLine, "Family")
	teoInDiet := strings.Index(dietLine, "Teo")
	if familyInDiet == -1 || teoInDiet == -1 {
		t.Fatalf("expected both Family and Teo in dietary line; got: %q", dietLine)
	}
	if familyInDiet > teoInDiet {
		t.Errorf("Family (%d) should come before Teo (%d) in dietary line: %q",
			familyInDiet, teoInDiet, dietLine)
	}

	// Determinism: render twice and compare.
	out2 := snap.Render()
	if out != out2 {
		t.Error("Render output is not deterministic")
	}
}

// ── AlwaysInjectedSnapshot: orphan exclusion ──────────────────────────────────

// TestAlwaysInjectedSnapshot_OrphanExcluded verifies that facts whose subject
// is not in knownSubjects (and is not "family") are excluded from the result.
func TestAlwaysInjectedSnapshot_OrphanExcluded(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	// Seed facts: two valid + one orphan + one non-injected.
	for _, f := range []familystate.Fact{
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
		{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher", CreatedBy: "dep"},
		// pets has always_inject=0 — should be excluded regardless.
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat", CreatedBy: "dep"},
		// ghost is an orphan subject not in knownSubjects — should be excluded.
		{Category: "allergies", Subject: "ghost", Label: "x", Value: "y", CreatedBy: "dep"},
	} {
		ff := f
		if err := s.UpsertFact(ctx, &ff); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	known := map[string]bool{"teo": true, "family": true}
	snap, err := s.AlwaysInjectedSnapshot(ctx, known)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// allergies should have exactly 1 row (teo), not ghost.
	allergyRows := snap.InjectedByCategory["allergies"]
	if len(allergyRows) != 1 {
		t.Fatalf("allergies: got %d rows, want 1", len(allergyRows))
	}
	if allergyRows[0].Subject != "teo" {
		t.Errorf("allergies row subject = %q, want %q", allergyRows[0].Subject, "teo")
	}

	// pets should not appear (always_inject=0).
	if _, ok := snap.InjectedByCategory["pets"]; ok {
		t.Error("pets should not appear in always-injected snapshot")
	}

	// dietary_restrictions should have 1 row (family).
	dietRows := snap.InjectedByCategory["dietary_restrictions"]
	if len(dietRows) != 1 {
		t.Fatalf("dietary_restrictions: got %d rows, want 1", len(dietRows))
	}
}
