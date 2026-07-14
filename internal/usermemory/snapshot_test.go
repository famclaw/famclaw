package usermemory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

func TestSnapshot_Render(t *testing.T) {
	now := time.Now()

	snap := &Snapshot{
		MemoriesByCategory: map[string][]Memory{
			"preferences": {
				{Label: "coffee", Value: "black", CreatedAt: now, UpdatedAt: now},
				{Label: "theme", Value: "dark", CreatedAt: now, UpdatedAt: now},
			},
			"projects": {
				{Label: "website", Value: "personal site", CreatedAt: now, UpdatedAt: now},
			},
		},
	}

	rendered := snap.Render()
	if rendered == "" {
		t.Fatal("expected non-empty render")
	}
	if !strings.Contains(rendered, "<user_memory>") {
		t.Error("expected <user_memory> tag")
	}
	if !strings.Contains(rendered, "</user_memory>") {
		t.Error("expected </user_memory> tag")
	}
	if !strings.Contains(rendered, "Preferences") {
		t.Error("expected Preferences category label")
	}
	if !strings.Contains(rendered, "coffee") {
		t.Error("expected coffee label")
	}
	if !strings.Contains(rendered, "black") {
		t.Error("expected black value")
	}
	if !strings.Contains(rendered, "Projects") {
		t.Error("expected Projects category label")
	}
	if !strings.Contains(rendered, "website") {
		t.Error("expected website label")
	}

	// Test empty snapshot
	emptySnap := &Snapshot{MemoriesByCategory: map[string][]Memory{}}
	if emptySnap.Render() != "" {
		t.Error("expected empty string for empty snapshot")
	}

	// Test nil snapshot
	var nilSnap *Snapshot
	if nilSnap.Render() != "" {
		t.Error("expected empty string for nil snapshot")
	}

	// Test unavailable snapshot
	unavailSnap := UnavailableSnapshot()
	rendered = unavailSnap.Render()
	if rendered == "" {
		t.Fatal("expected non-empty render for unavailable snapshot")
	}
	if !strings.Contains(rendered, "unavailable") {
		t.Error("expected unavailable notice in render")
	}
}

func TestStore_AlwaysInjectedSnapshot(t *testing.T) {
	dbPath := "/tmp/usermemory_snap_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	// Add memories for alice
	for _, m := range []*Memory{
		{UserName: "alice", Category: "preferences", Label: "coffee", Value: "black"},
		{UserName: "alice", Category: "preferences", Label: "theme", Value: "dark"},
		{UserName: "alice", Category: "projects", Label: "website", Value: "personal site"},
		{UserName: "bob", Category: "reminders", Label: "call mom", Value: "sunday"},
	} {
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Get snapshot for alice
	snap, err := s.AlwaysInjectedSnapshot(ctx, "alice")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.MemoriesByCategory) != 2 {
		t.Errorf("expected 2 categories, got %d", len(snap.MemoriesByCategory))
	}
	if len(snap.MemoriesByCategory["preferences"]) != 2 {
		t.Errorf("expected 2 preferences, got %d", len(snap.MemoriesByCategory["preferences"]))
	}
	if len(snap.MemoriesByCategory["projects"]) != 1 {
		t.Errorf("expected 1 project, got %d", len(snap.MemoriesByCategory["projects"]))
	}

	// Verify bob's memories are not in alice's snapshot
	if _, ok := snap.MemoriesByCategory["reminders"]; ok {
		t.Error("bob's reminders should not be in alice's snapshot")
	}

	// Verify render output
	rendered := snap.Render()
	if !strings.Contains(rendered, "coffee") || !strings.Contains(rendered, "black") {
		t.Error("rendered snapshot missing coffee memory")
	}
	if !strings.Contains(rendered, "website") {
		t.Error("rendered snapshot missing website memory")
	}

	// Test snapshot for user with no memories
	emptySnap, err := s.AlwaysInjectedSnapshot(ctx, "charlie")
	if err != nil {
		t.Fatalf("empty snapshot: %v", err)
	}
	if !emptySnap.IsEmpty() {
		t.Error("expected empty snapshot for user with no memories")
	}
	if emptySnap.Render() != "" {
		t.Error("expected empty render for empty snapshot")
	}
}