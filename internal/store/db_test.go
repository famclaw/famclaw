package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) (*DB, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}

func TestUnknownAccounts(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*testing.T)
	}{
		{
			name: "initial empty",
			fn: func(t *testing.T) {
				db, cleanup := newTestDB(t)
				defer cleanup()
				ctx := context.Background()
				accounts, err := db.ListUnknownAccounts(ctx)
				if err != nil {
					t.Fatalf("ListUnknownAccounts: %v", err)
				}
				if len(accounts) != 0 {
					t.Errorf("expected 0 accounts, got %d", len(accounts))
				}
			},
		},
		{
			name: "record creates row (lowercased gateway)",
			fn: func(t *testing.T) {
				db, cleanup := newTestDB(t)
				defer cleanup()
				ctx := context.Background()
				if err := db.RecordUnknownAccount(ctx, "Telegram", "532190216", "Julia"); err != nil {
					t.Fatalf("RecordUnknownAccount: %v", err)
				}
				accounts, err := db.ListUnknownAccounts(ctx)
				if err != nil {
					t.Fatalf("ListUnknownAccounts: %v", err)
				}
				if len(accounts) != 1 {
					t.Fatalf("expected 1 account, got %d", len(accounts))
				}
				acc := accounts[0]
				if acc.Gateway != "telegram" {
					t.Errorf("Gateway = %q, want %q", acc.Gateway, "telegram")
				}
				if acc.ExternalID != "532190216" {
					t.Errorf("ExternalID = %q, want %q", acc.ExternalID, "532190216")
				}
				if acc.DisplayName != "Julia" {
					t.Errorf("DisplayName = %q, want %q", acc.DisplayName, "Julia")
				}
				if acc.Attempts != 1 {
					t.Errorf("Attempts = %d, want 1", acc.Attempts)
				}
			},
		},
		{
			name: "record same key increments attempts and preserves display name",
			fn: func(t *testing.T) {
				db, cleanup := newTestDB(t)
				defer cleanup()
				ctx := context.Background()
				if err := db.RecordUnknownAccount(ctx, "Telegram", "532190216", "Julia"); err != nil {
					t.Fatalf("first RecordUnknownAccount: %v", err)
				}
				if err := db.RecordUnknownAccount(ctx, "Telegram", "532190216", ""); err != nil {
					t.Fatalf("second RecordUnknownAccount: %v", err)
				}
				accounts, err := db.ListUnknownAccounts(ctx)
				if err != nil {
					t.Fatalf("ListUnknownAccounts: %v", err)
				}
				if len(accounts) != 1 {
					t.Fatalf("expected 1 account, got %d", len(accounts))
				}
				acc := accounts[0]
				if acc.Attempts != 2 {
					t.Errorf("Attempts = %d, want 2", acc.Attempts)
				}
				if acc.DisplayName != "Julia" {
					t.Errorf("DisplayName = %q, want %q (must not be overwritten by empty)", acc.DisplayName, "Julia")
				}
			},
		},
		{
			name: "delete removes row",
			fn: func(t *testing.T) {
				db, cleanup := newTestDB(t)
				defer cleanup()
				ctx := context.Background()
				if err := db.RecordUnknownAccount(ctx, "Telegram", "532190216", "Julia"); err != nil {
					t.Fatalf("RecordUnknownAccount: %v", err)
				}
				if err := db.DeleteUnknownAccount(ctx, "Telegram", "532190216"); err != nil {
					t.Fatalf("DeleteUnknownAccount: %v", err)
				}
				accounts, err := db.ListUnknownAccounts(ctx)
				if err != nil {
					t.Fatalf("ListUnknownAccounts: %v", err)
				}
				if len(accounts) != 0 {
					t.Errorf("expected 0 accounts after delete, got %d", len(accounts))
				}
			},
		},
		{
			name: "delete missing is no-op",
			fn: func(t *testing.T) {
				db, cleanup := newTestDB(t)
				defer cleanup()
				ctx := context.Background()
				if err := db.DeleteUnknownAccount(ctx, "Telegram", "532190216"); err != nil {
					t.Errorf("DeleteUnknownAccount on missing: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.fn)
	}
}
