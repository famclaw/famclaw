package usermemory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

func TestStore_UpsertAndListMemories(t *testing.T) {
	dbPath := "/tmp/usermemory_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	// Test UpsertMemory
	m1 := &Memory{
		UserName: "alice",
		Category: "preferences",
		Label:    "coffee",
		Value:    "black, no sugar",
	}
	if err := s.UpsertMemory(ctx, m1); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m1.ID == 0 {
		t.Error("expected ID to be set after upsert")
	}
	if m1.CreatedAt.IsZero() || m1.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be set")
	}

	// Test upsert updates existing
	m2 := &Memory{
		UserName: "alice",
		Category: "preferences",
		Label:    "coffee",
		Value:    "oat milk latte",
	}
	if err := s.UpsertMemory(ctx, m2); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if m2.ID != m1.ID {
		t.Errorf("expected same ID %d on update, got %d", m1.ID, m2.ID)
	}
	if m2.Value != "oat milk latte" {
		t.Errorf("expected updated value, got %q", m2.Value)
	}

	// Test ListMemories
	memories, err := s.ListMemories(ctx, "alice", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(memories) != 1 {
		t.Errorf("expected 1 memory, got %d", len(memories))
	}
	if memories[0].Value != "oat milk latte" {
		t.Errorf("expected updated value, got %q", memories[0].Value)
	}

	// Add another memory in different category
	m3 := &Memory{
		UserName: "alice",
		Category: "projects",
		Label:    "website",
		Value:    "building a personal site",
	}
	if err := s.UpsertMemory(ctx, m3); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	memories, err = s.ListMemories(ctx, "alice", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(memories) != 2 {
		t.Errorf("expected 2 memories, got %d", len(memories))
	}

	// Test category filter
	memories, err = s.ListMemories(ctx, "alice", "preferences")
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(memories) != 1 {
		t.Errorf("expected 1 memory in preferences, got %d", len(memories))
	}
	if memories[0].Category != "preferences" {
		t.Errorf("expected category preferences, got %q", memories[0].Category)
	}

	// Test user isolation - bob should not see alice's memories
	m4 := &Memory{
		UserName: "bob",
		Category: "preferences",
		Label:    "tea",
		Value:    "green tea",
	}
	if err := s.UpsertMemory(ctx, m4); err != nil {
		t.Fatalf("upsert bob: %v", err)
	}

	aliceMemories, err := s.ListMemories(ctx, "alice", "")
	if err != nil {
		t.Fatalf("list alice: %v", err)
	}
	if len(aliceMemories) != 2 {
		t.Errorf("alice should have 2 memories, got %d", len(aliceMemories))
	}

	bobMemories, err := s.ListMemories(ctx, "bob", "")
	if err != nil {
		t.Fatalf("list bob: %v", err)
	}
	if len(bobMemories) != 1 {
		t.Errorf("bob should have 1 memory, got %d", len(bobMemories))
	}
	if bobMemories[0].Label != "tea" {
		t.Errorf("expected bob's memory to be tea, got %q", bobMemories[0].Label)
	}
}

func TestStore_DeleteMemory(t *testing.T) {
	dbPath := "/tmp/usermemory_delete_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	m := &Memory{
		UserName: "alice",
		Category: "preferences",
		Label:    "coffee",
		Value:    "black",
	}
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Delete by ID
	if err := s.DeleteMemory(ctx, "alice", m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	memories, err := s.ListMemories(ctx, "alice", "")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(memories) != 0 {
		t.Errorf("expected 0 memories after delete, got %d", len(memories))
	}

	// Delete non-existent should be idempotent
	if err := s.DeleteMemory(ctx, "alice", 9999); err != nil {
		t.Errorf("delete non-existent should be idempotent: %v", err)
	}

	// Delete by label
	m2 := &Memory{
		UserName: "alice",
		Category: "projects",
		Label:    "website",
		Value:    "personal site",
	}
	if err := s.UpsertMemory(ctx, m2); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	if err := s.DeleteMemoryByKey(ctx, "alice", "projects", "website"); err != nil {
		t.Fatalf("delete by key: %v", err)
	}

	memories, err = s.ListMemories(ctx, "alice", "")
	if err != nil {
		t.Fatalf("list after delete by label: %v", err)
	}
	if len(memories) != 0 {
		t.Errorf("expected 0 memories after delete by label, got %d", len(memories))
	}
}

func TestStore_GetMemoryByKey(t *testing.T) {
	dbPath := "/tmp/usermemory_get_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	m := &Memory{
		UserName: "alice",
		Category: "preferences",
		Label:    "coffee",
		Value:    "black",
	}
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Get by key
	got, err := s.GetMemoryByKey(ctx, "alice", "preferences", "coffee")
	if err != nil {
		t.Fatalf("get by key: %v", err)
	}
	if got.ID != m.ID {
		t.Errorf("expected ID %d, got %d", m.ID, got.ID)
	}
	if got.Value != "black" {
		t.Errorf("expected value 'black', got %q", got.Value)
	}

	// Non-existent
	_, err = s.GetMemoryByKey(ctx, "alice", "preferences", "nonexistent")
	if err != nil && err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for non-existent, got %v", err)
	}
}

func TestStore_MemoryCategories(t *testing.T) {
	dbPath := "/tmp/usermemory_cat_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	// Add memories in different categories
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

	cats, err := s.MemoryCategories(ctx, "alice")
	if err != nil {
		t.Fatalf("categories: %v", err)
	}
	if len(cats) != 2 {
		t.Errorf("expected 2 categories for alice, got %d: %v", len(cats), cats)
	}
	// Should be sorted
	if cats[0] != "preferences" || cats[1] != "projects" {
		t.Errorf("expected [preferences projects], got %v", cats)
	}

	bobCats, err := s.MemoryCategories(ctx, "bob")
	if err != nil {
		t.Fatalf("bob categories: %v", err)
	}
	if len(bobCats) != 1 || bobCats[0] != "reminders" {
		t.Errorf("expected [reminders] for bob, got %v", bobCats)
	}
}

// TestHandleForget tests the HandleForget function for both existing and non-existent memories.
func TestHandleForget(t *testing.T) {
	dbPath := "/tmp/usermemory_forget_test_" + time.Now().Format("20060102150405") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	ctx := context.Background()
	userName := "testuser"

	// Test forgetting a non-existent memory (should return "No memory found" message)
	msg, err := HandleForget(ctx, store, userName, "category", "label")
	if err != nil {
		t.Fatalf("unexpected error when forgetting non-existent memory: %v", err)
	}
	expectedMsg := fmt.Sprintf("No memory found with category %q and label %q.", "category", "label")
	if msg != expectedMsg {
		t.Errorf("expected %q, got %q", expectedMsg, msg)
	}

	// Test forgetting an existing memory
	// First, insert a memory
	m := &Memory{
		UserName: userName,
		Category: "category",
		Label:    "label",
		Value:    "value to forget",
	}
	if err := store.UpsertMemory(ctx, m); err != nil {
		t.Fatalf("upsert memory: %v", err)
	}

	// Verify the memory exists
	memories, err := store.ListMemories(ctx, userName, "")
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(memories) != 1 {
		t.Errorf("expected 1 memory after upsert, got %d", len(memories))
	}

	// Now forget it
	msg, err = HandleForget(ctx, store, userName, "category", "label")
	if err != nil {
		t.Fatalf("unexpected error when forgetting existing memory: %v", err)
	}
	if msg != "ok — forgotten" {
		t.Errorf("expected 'ok — forgotten', got %q", msg)
	}

	// Verify the memory is actually gone
	memories, err = store.ListMemories(ctx, userName, "")
	if err != nil {
		t.Fatalf("list memories after forget: %v", err)
	}
	if len(memories) != 0 {
		t.Errorf("expected 0 memories after forget, got %d", len(memories))
	}
}

func TestStore_UpsertEnforcesPerUserCap(t *testing.T) {
	dbPath := "/tmp/usermemory_evict_test_" + time.Now().Format("20060102150405.000000") + ".db"
	defer os.Remove(dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := NewStore(db)
	ctx := context.Background()

	// Upsert well over the cap for one user; distinct labels => distinct rows.
	total := maxMemoriesPerUser + 25
	for i := 0; i < total; i++ {
		m := &Memory{
			UserName: "dep",
			Category: "fact",
			Label:    fmt.Sprintf("k%04d", i),
			Value:    fmt.Sprintf("v%d", i),
		}
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	got, err := s.ListMemories(ctx, "dep", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != maxMemoriesPerUser {
		t.Fatalf("per-user cap not enforced: got %d memories, want %d", len(got), maxMemoriesPerUser)
	}
	// Survivors must be the most-recently-upserted (highest labels); the oldest were evicted.
	cutoff := fmt.Sprintf("k%04d", total-maxMemoriesPerUser)
	for _, m := range got {
		if m.Label < cutoff {
			t.Errorf("evicted wrong rows: found stale label %q (should have been evicted)", m.Label)
		}
	}
}
