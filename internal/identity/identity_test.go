package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/store"
)

func setupStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestLinkAndResolve(t *testing.T) {
	s := setupStore(t)

	tests := []struct {
		name       string
		userName   string
		gateway    string
		externalID string
	}{
		{"telegram user", "alice", "telegram", "12345"},
		{"whatsapp user", "bob", "whatsapp", "447911123456"},
		{"discord user", "charlie", "discord", "987654321"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.LinkAccount(tt.userName, tt.gateway, tt.externalID); err != nil {
				t.Fatalf("LinkAccount: %v", err)
			}

			user, err := s.Resolve(tt.gateway, tt.externalID)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if user == nil {
				t.Fatal("expected user, got nil")
			}
			if user.Name != tt.userName {
				t.Errorf("user.Name = %q, want %q", user.Name, tt.userName)
			}
		})
	}
}

func TestResolveUnknown(t *testing.T) {
	s := setupStore(t)

	user, err := s.Resolve("telegram", "nonexistent")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if user != nil {
		t.Errorf("expected nil user for unknown account, got %+v", user)
	}
}

func TestIsRegistered(t *testing.T) {
	s := setupStore(t)

	if s.IsRegistered("telegram", "12345") {
		t.Error("should not be registered before linking")
	}

	s.LinkAccount("alice", "telegram", "12345")

	if !s.IsRegistered("telegram", "12345") {
		t.Error("should be registered after linking")
	}
}

func TestGatewayNormalization(t *testing.T) {
	s := setupStore(t)

	s.LinkAccount("alice", "TELEGRAM", "12345")

	user, err := s.Resolve("telegram", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if user == nil {
		t.Fatal("expected user after case-insensitive gateway lookup")
	}
	if user.Name != "alice" {
		t.Errorf("user.Name = %q, want alice", user.Name)
	}
}

func TestUniquenessConstraint(t *testing.T) {
	s := setupStore(t)

	// Link alice to telegram:12345
	s.LinkAccount("alice", "telegram", "12345")

	// Re-link same external_id to bob — should update
	s.LinkAccount("bob", "telegram", "12345")

	user, err := s.Resolve("telegram", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if user == nil || user.Name != "bob" {
		t.Errorf("expected bob after re-link, got %+v", user)
	}
}

func TestSameUserMultipleGateways(t *testing.T) {
	s := setupStore(t)

	s.LinkAccount("alice", "telegram", "111")
	s.LinkAccount("alice", "discord", "222")

	u1, _ := s.Resolve("telegram", "111")
	u2, _ := s.Resolve("discord", "222")

	if u1 == nil || u1.Name != "alice" {
		t.Error("telegram should resolve to alice")
	}
	if u2 == nil || u2.Name != "alice" {
		t.Error("discord should resolve to alice")
	}
}

func TestUnlink(t *testing.T) {
	s := setupStore(t)

	s.LinkAccount("alice", "telegram", "12345")
	s.Unlink("telegram", "12345")

	user, err := s.Resolve("telegram", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if user != nil {
		t.Error("expected nil after unlink")
	}
}

func TestOnboardingMessages(t *testing.T) {
	msg := OnboardingMessage()
	if msg == "" {
		t.Error("OnboardingMessage should not be empty")
	}

	msg2 := UnknownAccountMessage()
	if msg2 == "" {
		t.Error("UnknownAccountMessage should not be empty")
	}
}

func TestConcurrentResolve(t *testing.T) {
	s := setupStore(t)
	s.LinkAccount("alice", "telegram", "12345")

	// Run concurrent resolves — should not panic or error
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			user, err := s.Resolve("telegram", "12345")
			if err != nil {
				t.Errorf("concurrent Resolve error: %v", err)
			}
			if user == nil || user.Name != "alice" {
				t.Errorf("concurrent Resolve: unexpected result %+v", user)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestStore_UnknownAccountLifecycle(t *testing.T) {
	cases := []struct {
		name        string
		recordGW    string
		recordID    string
		recordName  string
		expectAfter int
		clearGW     string
		clearID     string
		expectClear int
	}{
		{
			name:        "record then clear matching",
			recordGW:    "telegram",
			recordID:    "X1",
			recordName:  "Julia",
			expectAfter: 1,
			clearGW:     "telegram",
			clearID:     "X1",
			expectClear: 0,
		},
		{
			name:        "record with mixed-case gateway lowercases on store",
			recordGW:    "Telegram",
			recordID:    "X2",
			recordName:  "Sam",
			expectAfter: 1,
			clearGW:     "telegram",
			clearID:     "X2",
			expectClear: 0,
		},
		{
			name:        "clear non-matching id leaves row in place",
			recordGW:    "telegram",
			recordID:    "X1",
			recordName:  "Julia",
			expectAfter: 1,
			clearGW:     "telegram",
			clearID:     "DOES_NOT_EXIST",
			expectClear: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := setupStore(t)
			ctx := context.Background()

			if err := s.RecordUnknown(ctx, tc.recordGW, tc.recordID, tc.recordName); err != nil {
				t.Fatalf("RecordUnknown: %v", err)
			}

			unknowns, err := s.ListUnknown(ctx)
			if err != nil {
				t.Fatalf("ListUnknown after record: %v", err)
			}
			if len(unknowns) != tc.expectAfter {
				t.Fatalf("len(unknowns) after record = %d, want %d", len(unknowns), tc.expectAfter)
			}
			if tc.expectAfter > 0 && unknowns[0].DisplayName != tc.recordName {
				t.Errorf("DisplayName = %q, want %q", unknowns[0].DisplayName, tc.recordName)
			}

			if err := s.ClearUnknown(ctx, tc.clearGW, tc.clearID); err != nil {
				t.Fatalf("ClearUnknown: %v", err)
			}

			unknowns, err = s.ListUnknown(ctx)
			if err != nil {
				t.Fatalf("ListUnknown after clear: %v", err)
			}
			if len(unknowns) != tc.expectClear {
				t.Errorf("len(unknowns) after clear = %d, want %d", len(unknowns), tc.expectClear)
			}
		})
	}
}

// Prevent unused import error for os
var _ = os.DevNull
