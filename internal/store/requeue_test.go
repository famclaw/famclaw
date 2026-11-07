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

// TestRequeueApproval_ValidGrantUntouched guards the self-contained expiry
// check: RequeueApproval must NOT reset a still-valid approved row to
// pending — a caller bug or a future unconditional call path must not revoke
// the parent's active grant.
func TestRequeueApproval_ValidGrantUntouched(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	_, err := db.UpsertApproval(ctx, &Approval{
		ID:          "requeue-valid-1",
		UserName:    "jdoe",
		UserDisplay: "J Doe",
		AgeGroup:    "teen",
		Category:    "suspicious-site",
		QueryText:   "example-host.com",
	})
	if err != nil {
		t.Fatalf("UpsertApproval: %v", err)
	}
	// Fresh grant: the default 24h window keeps expires_at in the future.
	if err := db.DecideApproval("requeue-valid-1", "approved", "parent"); err != nil {
		t.Fatalf("DecideApproval(approved): %v", err)
	}
	before, err := db.GetApproval(ctx, "requeue-valid-1")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if before == nil {
		t.Fatal("GetApproval returned nil, want row")
	}

	requeued, err := db.RequeueApproval(ctx, "requeue-valid-1")
	if err != nil {
		t.Fatalf("RequeueApproval: %v", err)
	}
	if requeued {
		t.Fatalf("RequeueApproval returned true, want false for a still-valid approved row")
	}

	after, err := db.GetApproval(ctx, "requeue-valid-1")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if after == nil {
		t.Fatal("GetApproval returned nil, want row")
	}
	if after.Status != "approved" {
		t.Errorf("Status = %q, want approved (untouched)", after.Status)
	}
	if after.DecidedBy != "parent" {
		t.Errorf("DecidedBy = %q, want parent (untouched)", after.DecidedBy)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Errorf("ExpiresAt changed from %v to %v, want untouched", before.ExpiresAt, after.ExpiresAt)
	}
	if !after.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want still in the future", after.ExpiresAt)
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
