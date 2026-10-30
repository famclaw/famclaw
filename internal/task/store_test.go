package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// TestSharedTaskTimestampsRoundTrip guards against scanning the INTEGER
// created_at/updated_at columns straight into time.Time. database/sql rejects
// an int64 -> time.Time conversion, so GetSharedTask/ListSharedTasks used to
// error on every read once a row was present.
func TestSharedTaskTimestampsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s := NewStore(db)
	ctx := context.Background()

	due := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	added, err := s.AddSharedTask(ctx, "owner-a", "do the thing", "pm-regression", due)
	if err != nil {
		t.Fatalf("AddSharedTask: %v", err)
	}

	got, err := s.GetSharedTask(ctx, added.ID)
	if err != nil {
		t.Fatalf("GetSharedTask: %v", err)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero after round-trip (int64 -> time.Time scan broken)")
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt is zero after round-trip (int64 -> time.Time scan broken)")
	}
	if !got.DueAt.Equal(due) {
		t.Errorf("DueAt = %v, want %v", got.DueAt, due)
	}

	list, err := s.ListSharedTasks(ctx, "owner-a", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListSharedTasks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSharedTasks returned %d rows, want 1", len(list))
	}
	if list[0].CreatedAt.IsZero() || list[0].UpdatedAt.IsZero() {
		t.Errorf("list row has zero timestamps: %+v", list[0])
	}
}

// TestSharedTaskTransitionsNoMatch guards the silent no-op regression:
// AcceptTask/DeclineTask/CompleteTask/HandBackTask previously returned nil
// when their UPDATE matched zero rows (unknown id, or task owned by someone
// else), so callers could not surface "task not found" or "not your task".
// Each transition must now return a wrapped ErrTaskNotFound in the
// zero-rows case, and still apply the state change on a matching row.
func TestSharedTaskTransitionsNoMatch(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s := NewStore(db)
	ctx := context.Background()

	due := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	added, err := s.AddSharedTask(ctx, "owner-a", "do the thing", "pm-regression", due)
	if err != nil {
		t.Fatalf("AddSharedTask: %v", err)
	}

	// Unknown task id: every transition must report not-found, not no-op.
	missing := int64(999999)
	ops := map[string]func() error{
		"AcceptTask":   func() error { return s.AcceptTask(ctx, "owner-a", missing) },
		"DeclineTask":  func() error { return s.DeclineTask(ctx, "owner-a", missing) },
		"CompleteTask": func() error { return s.CompleteTask(ctx, "owner-a", missing) },
		"HandBackTask": func() error { return s.HandBackTask(ctx, "owner-a", missing) },
	}
	for name, op := range ops {
		err := op()
		if err == nil {
			t.Errorf("%s(missing id): expected error, got nil (silent no-op)", name)
			continue
		}
		if !errors.Is(err, ErrTaskNotFound) {
			t.Errorf("%s(missing id): error = %v, want wrapped ErrTaskNotFound", name, err)
		}
	}

	// Task exists but belongs to a different owner: same class of no-match.
	for name, op := range map[string]func() error{
		"AcceptTask": func() error { return s.AcceptTask(ctx, "owner-b", added.ID) },
	} {
		err := op()
		if err == nil {
			t.Errorf("%s(wrong owner): expected error, got nil (silent no-op)", name)
			continue
		}
		if !errors.Is(err, ErrTaskNotFound) {
			t.Errorf("%s(wrong owner): error = %v, want wrapped ErrTaskNotFound", name, err)
		}
	}

	// Matching row: the transition still applies and returns nil.
	if err := s.AcceptTask(ctx, "owner-a", added.ID); err != nil {
		t.Fatalf("AcceptTask(matching): %v", err)
	}
	got, err := s.GetSharedTask(ctx, added.ID)
	if err != nil {
		t.Fatalf("GetSharedTask: %v", err)
	}
	if !got.Accepted {
		t.Errorf("Accepted = false after AcceptTask, want true")
	}
	if err := s.CompleteTask(ctx, "owner-a", added.ID); err != nil {
		t.Fatalf("CompleteTask(matching): %v", err)
	}
	got, err = s.GetSharedTask(ctx, added.ID)
	if err != nil {
		t.Fatalf("GetSharedTask: %v", err)
	}
	if !got.Completed {
		t.Errorf("Completed = false after CompleteTask, want true")
	}
}
