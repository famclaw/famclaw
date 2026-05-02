package store

import (
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
	db, cleanup := newTestDB(t)
	defer cleanup()

	accounts, err := db.ListUnknownAccounts()
	if err != nil {
		t.Fatalf("ListUnknownAccounts initial: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("Expected 0 accounts, got %d", len(accounts))
	}

	if err := db.RecordUnknownAccount("Telegram", "532190216", "Julia"); err != nil {
		t.Fatalf("RecordUnknownAccount: %v", err)
	}
	accounts, err = db.ListUnknownAccounts()
	if err != nil {
		t.Fatalf("ListUnknownAccounts after record: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("Expected 1 account, got %d", len(accounts))
	}
	acc := accounts[0]
	if acc.Gateway != "telegram" {
		t.Errorf("Gateway expected 'telegram', got '%s'", acc.Gateway)
	}
	if acc.ExternalID != "532190216" {
		t.Errorf("ExternalID expected '532190216', got '%s'", acc.ExternalID)
	}
	if acc.DisplayName != "Julia" {
		t.Errorf("DisplayName expected 'Julia', got '%s'", acc.DisplayName)
	}
	if acc.Attempts != 1 {
		t.Errorf("Attempts expected 1, got %d", acc.Attempts)
	}

	if err := db.RecordUnknownAccount("Telegram", "532190216", ""); err != nil {
		t.Fatalf("RecordUnknownAccount second: %v", err)
	}
	accounts, err = db.ListUnknownAccounts()
	if err != nil {
		t.Fatalf("ListUnknownAccounts after second record: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("Expected 1 account, got %d", len(accounts))
	}
	acc = accounts[0]
	if acc.Attempts != 2 {
		t.Errorf("Attempts expected 2, got %d", acc.Attempts)
	}
	if acc.DisplayName != "Julia" {
		t.Errorf("DisplayName should remain 'Julia', got '%s'", acc.DisplayName)
	}

	if err := db.DeleteUnknownAccount("Telegram", "532190216"); err != nil {
		t.Fatalf("DeleteUnknownAccount: %v", err)
	}
	accounts, err = db.ListUnknownAccounts()
	if err != nil {
		t.Fatalf("ListUnknownAccounts after delete: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("Expected 0 accounts after delete, got %d", len(accounts))
	}

	if err := db.DeleteUnknownAccount("Telegram", "532190216"); err != nil {
		t.Errorf("DeleteUnknownAccount on missing: %v", err)
	}
}
