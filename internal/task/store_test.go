package task

import (
	"context"
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
