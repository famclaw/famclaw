package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentDecisionSequenceAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, id := range []string{"a", "b"} {
		if _, err := db.UpsertApproval(ctx, &Approval{ID: id, UserName: "kid"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- db.DecideApproval("a", "denied", "parent") }()
	go func() { defer wg.Done(); errs <- other.DecideApprovalWithNote(ctx, "b", "approved", "parent", "ok") }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var a, b int64
	if err := db.SQL().QueryRow(`SELECT decision_sequence FROM approvals WHERE id='a'`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT decision_sequence FROM approvals WHERE id='b'`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a <= 0 || b <= 0 || a == b {
		t.Fatalf("sequences a=%d b=%d", a, b)
	}
	if err := db.DecideApproval("a", "approved", "parent"); err == nil {
		t.Fatal("already decided row accepted")
	}
	var after int64
	if err := db.SQL().QueryRow(`SELECT decision_sequence FROM approvals WHERE id='a'`).Scan(&after); err != nil || after != a {
		t.Fatalf("sequence changed: %d %v", after, err)
	}
}
