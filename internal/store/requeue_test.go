package store

import (
	"context"
	"testing"
	"time"
)

func TestRequeueApproval_ExpiredGrant(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	_, err := db.UpsertApproval(ctx, &Approval{
		ID:          "requeue-expired-1",
		UserName:    "jdoe",
		UserDisplay: "J Doe",
		AgeGroup:    "teen",
		Category:    "suspicious-site",
		QueryText:   "example-host.com",
	})
	if err != nil {
		t.Fatalf("UpsertApproval: %v", err)
	}
	if err := db.DecideApproval("requeue-expired-1", "approved", "parent"); err != nil {
		t.Fatalf("DecideApproval(approved): %v", err)
	}

	// Backdate the grant so it is expired.
	if _, err := db.sql.Exec(`UPDATE approvals SET expires_at = ? WHERE id = 'requeue-expired-1'`, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	requeued, err := db.RequeueApproval(ctx, "requeue-expired-1")
	if err != nil {
		t.Fatalf("RequeueApproval: %v", err)
	}
	if !requeued {
		t.Fatalf("RequeueApproval returned false, want true for expired approved row")
	}

	got, err := db.GetApproval(ctx, "requeue-expired-1")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got == nil {
		t.Fatal("GetApproval returned nil, want row")
	}
	if got.Status != "pending" {
		t.Errorf("Status = %q, want pending", got.Status)
	}
	if got.DecidedBy != "" {
		t.Errorf("DecidedBy = %q, want empty", got.DecidedBy)
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want in the future", got.ExpiresAt)
	}
}

func TestRequeueApproval_DenialUntouched(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	_, err := db.UpsertApproval(ctx, &Approval{
		ID:          "requeue-denied-1",
		UserName:    "jdoe",
		UserDisplay: "J Doe",
		AgeGroup:    "teen",
		Category:    "suspicious-site",
		QueryText:   "example-host.com",
	})
	if err != nil {
		t.Fatalf("UpsertApproval: %v", err)
	}
	if err := db.DecideApproval("requeue-denied-1", "denied", "parent"); err != nil {
		t.Fatalf("DecideApproval(denied): %v", err)
	}

	// Backdate the expiry.
	if _, err := db.sql.Exec(`UPDATE approvals SET expires_at = ? WHERE id = 'requeue-denied-1'`, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	requeued, err := db.RequeueApproval(ctx, "requeue-denied-1")
	if err != nil {
		t.Fatalf("RequeueApproval: %v", err)
	}
	if requeued {
		t.Fatalf("RequeueApproval returned true, want false for denied row")
	}

	got, err := db.GetApproval(ctx, "requeue-denied-1")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got == nil {
		t.Fatal("GetApproval returned nil, want row")
	}
	if got.Status != "denied" {
		t.Errorf("Status = %q, want denied", got.Status)
	}
}
