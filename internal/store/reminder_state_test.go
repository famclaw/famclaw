package store

// Regression tests for the truthful reminder delivery-state machine
// (terminal-state fix): pending → delivered | undelivered, with the bounded
// undelivered_retry state in between, plus the one-time legacy-data
// migration that maps old dispatched=1 rows to 'delivered'.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newReminderForState creates a reminder and re-reads its persisted row
// (CreateReminder does not populate the ID).
func newReminderForState(t *testing.T, db *DB, msg string) *Reminder {
	t.Helper()
	ctx := context.Background()
	r := &Reminder{
		UserName:   "julia",
		Gateway:    "telegram",
		ExternalID: "123",
		Message:    msg,
		DueAt:      time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateReminder(ctx, r); err != nil {
		t.Fatalf("CreateReminder: %v", err)
	}
	var state, reason string
	var attempts int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT id, delivery_state, terminal_attempts, undelivered_reason FROM reminders WHERE message = ?`, msg).
		Scan(&r.ID, &state, &attempts, &reason); err != nil {
		t.Fatalf("re-read created reminder: %v", err)
	}
	r.DeliveryState = state
	r.TerminalAttempts = attempts
	r.UndeliveredReason = reason
	return r
}

func TestReminderDeliveryStateTransitions(t *testing.T) {
	ctx := context.Background()
	db, cleanup := newTestDB(t)
	defer cleanup()

	var seq int
	new := func() *Reminder {
		seq++
		return newReminderForState(t, db, fmt.Sprintf("state transition %d", seq))
	}
	stateOf := func(id int64) (string, int, string) {
		t.Helper()
		var state, reason string
		var attempts int
		if err := db.sql.QueryRowContext(ctx,
			`SELECT delivery_state, terminal_attempts, undelivered_reason FROM reminders WHERE id = ?`, id).
			Scan(&state, &attempts, &reason); err != nil {
			t.Fatalf("read state: %v", err)
		}
		return state, attempts, reason
	}

	t.Run("create lands in pending and is due", func(t *testing.T) {
		r := new()
		if r.DeliveryState != ReminderStatePending {
			t.Fatalf("delivery_state = %q, want %q", r.DeliveryState, ReminderStatePending)
		}
		due, err := db.GetDueReminders(ctx, time.Now().UTC())
		if err != nil {
			t.Fatalf("GetDueReminders: %v", err)
		}
		found := false
		for _, d := range due {
			if d.ID == r.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("new reminder not in due queue: %+v", due)
		}
	})

	t.Run("delivered is terminal and leaves every queue", func(t *testing.T) {
		r := new()
		if err := db.MarkReminderDispatched(ctx, r.ID, time.Now().UTC()); err != nil {
			t.Fatalf("MarkReminderDispatched: %v", err)
		}
		state, attempts, reason := stateOf(r.ID)
		if state != ReminderStateDelivered {
			t.Fatalf("delivery_state = %q, want %q", state, ReminderStateDelivered)
		}
		if attempts != 0 || reason != "" {
			t.Fatalf("delivered row must not carry terminal accounting: attempts=%d reason=%q", attempts, reason)
		}
	})

	t.Run("undelivered_retry then undelivered, budget persisted", func(t *testing.T) {
		r := new()
		if err := db.BeginUndeliveredRetry(ctx, r.ID, "retries_exhausted"); err != nil {
			t.Fatalf("BeginUndeliveredRetry: %v", err)
		}
		if state, attempts, reason := stateOf(r.ID); state != ReminderStateUndeliveredRetry || attempts != 1 || reason != "retries_exhausted" {
			t.Fatalf("retry row = state %q attempts %d reason %q, want undelivered_retry/1/retries_exhausted", state, attempts, reason)
		}
		// The retry row must not re-enter the delivery queue.
		for _, d := range mustGetDue(t, db, ctx) {
			if d.ID == r.ID {
				t.Fatalf("retry row still in due queue")
			}
		}
		for i := 0; i < 2; i++ {
			if err := db.IncrementTerminalAttempt(ctx, r.ID); err != nil {
				t.Fatalf("IncrementTerminalAttempt %d: %v", i, err)
			}
		}
		if err := db.MarkUndelivered(ctx, r.ID, time.Now().UTC(), "retries_exhausted"); err != nil {
			t.Fatalf("MarkUndelivered: %v", err)
		}
		if state, attempts, reason := stateOf(r.ID); state != ReminderStateUndelivered || attempts != 3 || reason != "retries_exhausted" {
			t.Fatalf("terminal row = state %q attempts %d reason %q, want undelivered/3/retries_exhausted", state, attempts, reason)
		}
	})

	t.Run("false-delivery guards", func(t *testing.T) {
		// undelivered -> delivered must be impossible once terminal.
		r := new()
		if err := db.MarkUndelivered(ctx, r.ID, time.Now().UTC(), "invalid_chat"); err != nil {
			t.Fatalf("MarkUndelivered: %v", err)
		}
		if err := db.MarkReminderDispatched(ctx, r.ID, time.Now().UTC()); err != nil {
			t.Fatalf("MarkReminderDispatched (expect no-op): %v", err)
		}
		if state, _, _ := stateOf(r.ID); state != ReminderStateUndelivered {
			t.Fatalf("undelivered row re-labelled %q by MarkReminderDispatched", state)
		}

		// delivered -> retry/undelivered must be impossible once delivered.
		r2 := new()
		if err := db.MarkReminderDispatched(ctx, r2.ID, time.Now().UTC()); err != nil {
			t.Fatalf("MarkReminderDispatched: %v", err)
		}
		if err := db.BeginUndeliveredRetry(ctx, r2.ID, "retries_exhausted"); err != nil {
			t.Fatalf("BeginUndeliveredRetry (expect no-op): %v", err)
		}
		if err := db.MarkUndelivered(ctx, r2.ID, time.Now().UTC(), "invalid_chat"); err != nil {
			t.Fatalf("MarkUndelivered (expect no-op): %v", err)
		}
		if err := db.IncrementTerminalAttempt(ctx, r2.ID); err != nil {
			t.Fatalf("IncrementTerminalAttempt (expect no-op): %v", err)
		}
		if state, attempts, reason := stateOf(r2.ID); state != ReminderStateDelivered || attempts != 0 || reason != "" {
			t.Fatalf("delivered row mutated: state %q attempts %d reason %q", state, attempts, reason)
		}
	})
}

// mustGetDue is a test helper: GetDueReminders or fail.
func mustGetDue(t *testing.T, db *DB, ctx context.Context) []*Reminder {
	t.Helper()
	due, err := db.GetDueReminders(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("GetDueReminders: %v", err)
	}
	return due
}

// TestReminderLegacyDispatchedMigration simulates a pre-terminal-state
// deployment (reminders table without delivery_state) and verifies the
// one-time data migration: old dispatched=1 rows map to 'delivered' (they
// must not re-enter the delivery queue), old dispatched=0 rows stay
// 'pending'.
func TestReminderLegacyDispatchedMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	// Seed the OLD schema (as it existed before the terminal-state columns)
	// with one delivered and one still-pending reminder.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	oldSchema := `
		CREATE TABLE reminders (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			user_name      TEXT NOT NULL,
			gateway        TEXT NOT NULL,
			external_id    TEXT NOT NULL,
			group_id       TEXT DEFAULT '',
			is_group       INTEGER NOT NULL DEFAULT 0,
			message        TEXT NOT NULL,
			due_at         TEXT NOT NULL,
			dispatched     INTEGER NOT NULL DEFAULT 0,
			dispatched_at  TEXT DEFAULT '',
			created_at         TEXT NOT NULL,
			setter_gateway     TEXT DEFAULT '',
			setter_external_id TEXT DEFAULT '',
			delivery_attempts  INTEGER NOT NULL DEFAULT 0
		);`
	if _, err := raw.ExecContext(ctx, oldSchema); err != nil {
		raw.Close()
		t.Fatalf("create old reminders table: %v", err)
	}
	past := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO reminders (user_name, gateway, external_id, message, due_at, dispatched, dispatched_at, created_at)
		VALUES ('julia', 'telegram', '1', 'old delivered', ?, 1, ?, ?)`, past, past, past); err != nil {
		raw.Close()
		t.Fatalf("insert legacy delivered row: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO reminders (user_name, gateway, external_id, message, due_at, dispatched, created_at)
		VALUES ('julia', 'telegram', '2', 'old pending', ?, 0, ?)`, past, past); err != nil {
		raw.Close()
		t.Fatalf("insert legacy pending row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Now open with the real store: migrations must add the new columns and
	// map the legacy rows.
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer db.Close()

	// Legacy delivered row must not re-enter the queue; the legacy pending
	// row must still be deliverable.
	pending, err := db.GetPendingReminders(ctx)
	if err != nil {
		t.Fatalf("GetPendingReminders: %v", err)
	}
	if len(pending) != 1 || pending[0].Message != "old pending" {
		t.Fatalf("pending after migration = %+v, want exactly the legacy pending row", pending)
	}
	if pending[0].DeliveryState != ReminderStatePending {
		t.Fatalf("legacy pending row delivery_state = %q, want %q", pending[0].DeliveryState, ReminderStatePending)
	}
	for _, d := range mustGetDue(t, db, ctx) {
		if d.Message == "old delivered" {
			t.Fatalf("legacy delivered row re-entered the due queue")
		}
	}
	var state string
	if err := db.sql.QueryRowContext(ctx, `SELECT delivery_state FROM reminders WHERE message = ?`, "old delivered").Scan(&state); err != nil {
		t.Fatalf("read legacy delivered state: %v", err)
	}
	if state != ReminderStateDelivered {
		t.Fatalf("legacy dispatched=1 row delivery_state = %q, want %q", state, ReminderStateDelivered)
	}

	// The migration must be idempotent on a second open (fresh process,
	// columns already present).
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open (idempotent migrate): %v", err)
	}
	defer db2.Close()
	pending2, err := db2.GetPendingReminders(ctx)
	if err != nil || len(pending2) != 1 {
		t.Fatalf("pending after re-open = %d (err %v), want 1", len(pending2), err)
	}
}
