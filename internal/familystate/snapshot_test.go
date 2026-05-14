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

	for _, f := range []Fact{
		{Category: "allergies", Subject: "teo", Label: "peanuts", Value: "severe", CreatedBy: "dep"},
		{Category: "dietary_restrictions", Subject: "family", Label: "kosher", Value: "kosher", CreatedBy: "dep"},
		{Category: "pets", Subject: "family", Label: "Stella", Value: "cat", CreatedBy: "dep"},
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
	allergyRows := snap.InjectedByCategory["allergies"]
	if len(allergyRows) != 1 || allergyRows[0].Subject != "teo" {
		t.Errorf("allergies = %+v, want 1 row for teo", allergyRows)
	}
	if _, ok := snap.InjectedByCategory["pets"]; ok {
		t.Error("pets should not appear in always-injected snapshot")
	}
}
