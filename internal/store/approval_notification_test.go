package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestApprovalNotificationMigrationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE approvals (id TEXT PRIMARY KEY, user_name TEXT NOT NULL, user_display TEXT NOT NULL, age_group TEXT NOT NULL, category TEXT NOT NULL, query_text TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME NOT NULL, decided_by TEXT, decision_note TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`INSERT INTO approvals(id,user_name,user_display,age_group,category,query_text,expires_at) VALUES ('a','child','Child','age_5_12','suspicious_site','example.com',?)`, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls := 0
	notify := func() error { calls++; return nil }
	if err := db.NotifyApprovalOnce(ctx, ctx, "a", notify); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.NotifyApprovalOnce(ctx, ctx, "a", notify); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("notifications after reopen=%d want 1", calls)
	}
}

func TestApprovalNotificationPanicReleasesClaim(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.UpsertApproval(ctx, &Approval{ID: "a", UserName: "child", UserDisplay: "Child", AgeGroup: "age_5_12", Category: "suspicious_site", QueryText: "example.com"}); err != nil {
		t.Fatal(err)
	}
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_ = db.NotifyApprovalOnce(ctx, ctx, "a", func() error { panic("test panic") })
	}()
	if !panicked {
		t.Fatal("expected notifier panic")
	}
	calls := 0
	if err := db.NotifyApprovalOnce(ctx, ctx, "a", func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("notification retried %d times, want 1", calls)
	}
}

// Shutdown cancels a marker write waiting on the database.
func TestApprovalNotificationMarkerStopsOnShutdown(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.UpsertApproval(ctx, &Approval{ID: "a", UserName: "child", UserDisplay: "Child", AgeGroup: "age_5_12", Category: "suspicious_site", QueryText: "example.com"}); err != nil {
		t.Fatal(err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db.SQL().SetMaxOpenConns(1)
	held := make(chan *sql.Conn, 1)
	done := make(chan error, 1)
	go func() {
		done <- db.NotifyApprovalOnce(ctx, lifetime, "a", func() error {
			conn, err := db.SQL().Conn(ctx)
			if err != nil {
				return err
			}
			held <- conn
			return nil
		})
	}()
	var conn *sql.Conn
	select {
	case conn = <-held:
	case <-time.After(3 * time.Second):
		t.Fatal("notification did not acquire test connection")
	}
	t.Cleanup(func() { _ = conn.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for db.SQL().Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("marker write did not wait for database")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("marker write error = %v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("marker write did not stop after shutdown")
	}
}

// A stalled competing claim must not prevent a successful sender's marker
// write from observing server shutdown.
func TestApprovalNotificationStalledClaimDoesNotBlockShutdown(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.UpsertApproval(ctx, &Approval{ID: "a", UserName: "child", UserDisplay: "Child", AgeGroup: "age_5_12", Category: "suspicious_site", QueryText: "example.com"}); err != nil {
		t.Fatal(err)
	}
	lifetime, shutdown := context.WithCancel(ctx)
	other, stopOther := context.WithCancel(ctx)
	inSend := make(chan struct{})
	releaseSend := make(chan struct{})
	firstDone := make(chan error, 1)
	otherDone := make(chan error, 1)
	go func() {
		firstDone <- db.NotifyApprovalOnce(ctx, lifetime, "a", func() error { close(inSend); <-releaseSend; return nil })
	}()
	select {
	case <-inSend:
	case <-time.After(3 * time.Second):
		t.Fatal("notification did not start")
	}
	held, err := db.SQL().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdown(); stopOther(); _ = held.Close() })
	go func() { otherDone <- db.NotifyApprovalOnce(other, other, "a", func() error { return nil }) }()
	deadline := time.Now().Add(3 * time.Second)
	for db.SQL().Stats().WaitCount < 1 {
		if time.Now().After(deadline) {
			t.Fatal("competing read did not block")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseSend)
	shutdown()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("marker error=%v want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("marker blocked behind stalled claim mutex")
	}
	stopOther()
	select {
	case <-otherDone:
	case <-time.After(2 * time.Second):
		t.Fatal("competing read did not stop")
	}
}

func TestApprovalNotificationFailurePhase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase ApprovalNotificationPhase
	}{
		{"read", ApprovalNotificationRead}, {"send", ApprovalNotificationSend}, {"record", ApprovalNotificationRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lifetime, shutdown := context.WithCancel(context.Background())
			defer shutdown()
			if _, err := db.UpsertApproval(context.Background(), &Approval{ID: "a", UserName: "child", UserDisplay: "Child", AgeGroup: "age_5_12", Category: "suspicious_site", QueryText: "example.com"}); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("fake-credential-url")
			if tc.name == "read" {
				cancel()
				cause = context.Canceled
			}
			err = db.NotifyApprovalOnce(ctx, lifetime, "a", func() error {
				if tc.name == "record" {
					shutdown()
					cause = context.Canceled
					return nil
				}
				return cause
			})
			var failure *ApprovalNotificationError
			if !errors.As(err, &failure) || failure.Phase != tc.phase || !errors.Is(err, cause) {
				t.Fatalf("error=%v phase/cause mismatch", err)
			}
		})
	}
}
