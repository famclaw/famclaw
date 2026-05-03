package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
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

func TestStore_LinkAndClearUnknown(t *testing.T) {
	cases := []struct {
		name           string
		seedUnknown    bool
		seedGW         string
		seedExtID      string
		linkUserName   string
		linkGW         string
		linkExtID      string
		wantLinkErr    bool
		wantUserAfter  string
		wantUnknownLen int
	}{
		{
			name:           "happy path: link and clear in one tx",
			seedUnknown:    true,
			seedGW:         "telegram",
			seedExtID:      "X1",
			linkUserName:   "alice",
			linkGW:         "telegram",
			linkExtID:      "X1",
			wantUserAfter:  "alice",
			wantUnknownLen: 0,
		},
		{
			name:           "mixed-case gateway is lowercased on both writes",
			seedUnknown:    true,
			seedGW:         "telegram",
			seedExtID:      "X2",
			linkUserName:   "bob",
			linkGW:         "Telegram",
			linkExtID:      "X2",
			wantUserAfter:  "bob",
			wantUnknownLen: 0,
		},
		{
			name:           "no unknown row: link still succeeds, clear is no-op",
			seedUnknown:    false,
			linkUserName:   "carol",
			linkGW:         "discord",
			linkExtID:      "D1",
			wantUserAfter:  "carol",
			wantUnknownLen: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := setupStore(t)
			ctx := context.Background()

			if tc.seedUnknown {
				if err := s.RecordUnknown(ctx, tc.seedGW, tc.seedExtID, "Display"); err != nil {
					t.Fatalf("seed RecordUnknown: %v", err)
				}
			}

			err := s.LinkAndClearUnknown(ctx, tc.linkUserName, tc.linkGW, tc.linkExtID)
			if tc.wantLinkErr {
				if err == nil {
					t.Fatalf("LinkAndClearUnknown: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LinkAndClearUnknown: %v", err)
			}

			user, err := s.Resolve(tc.linkGW, tc.linkExtID)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if user == nil || user.Name != tc.wantUserAfter {
				t.Errorf("Resolve = %+v, want user %q", user, tc.wantUserAfter)
			}

			list, err := s.ListUnknown(ctx)
			if err != nil {
				t.Fatalf("ListUnknown: %v", err)
			}
			if len(list) != tc.wantUnknownLen {
				t.Errorf("len(unknown) = %d, want %d", len(list), tc.wantUnknownLen)
			}
		})
	}
}

func TestStore_UnlinkedUsers(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T, *Store, *config.Config)
	}{
		{
			name: "no links: all users returned",
			fn: func(t *testing.T, s *Store, cfg *config.Config) {
				users := s.UnlinkedUsers(cfg, "telegram")
				if len(users) != len(cfg.Users) {
					t.Errorf("expected %d users, got %d", len(cfg.Users), len(users))
				}
			},
		},
		{
			name: "one user linked: that user is excluded",
			fn: func(t *testing.T, s *Store, cfg *config.Config) {
				if err := s.LinkAccount("bob", "telegram", "B1"); err != nil {
					t.Fatal(err)
				}
				users := s.UnlinkedUsers(cfg, "telegram")
				if len(users) != len(cfg.Users)-1 {
					t.Errorf("expected %d users, got %d", len(cfg.Users)-1, len(users))
				}
				for _, u := range users {
					if u.Name == "bob" {
						t.Errorf("bob should not be in unlinked users")
					}
				}
			},
		},
		{
			name: "different gateway: irrelevant",
			fn: func(t *testing.T, s *Store, cfg *config.Config) {
				if err := s.LinkAccount("bob", "whatsapp", "W1"); err != nil {
					t.Fatal(err)
				}
				users := s.UnlinkedUsers(cfg, "telegram")
				if len(users) != len(cfg.Users) {
					t.Errorf("expected %d users for telegram (whatsapp link is irrelevant), got %d", len(cfg.Users), len(users))
				}
				found := false
				for _, u := range users {
					if u.Name == "bob" {
						found = true
					}
				}
				if !found {
					t.Errorf("bob should still be unlinked on telegram")
				}
			},
		},
	}
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "alice", DisplayName: "Alice", Role: "parent"},
			{Name: "bob", DisplayName: "Bob", Role: "child"},
			{Name: "carol", DisplayName: "Carol", Role: "child"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupStore(t)
			tt.fn(t, s, cfg)
		})
	}
}

func TestStore_ErrorPathsAfterClose(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T, *Store)
	}{
		{
			name: "RecordUnknown",
			fn: func(t *testing.T, s *Store) {
				if err := s.RecordUnknown(context.Background(), "telegram", "id", "display"); err == nil {
					t.Error("expected error, got nil")
				}
			},
		},
		{
			name: "ListUnknown",
			fn: func(t *testing.T, s *Store) {
				if _, err := s.ListUnknown(context.Background()); err == nil {
					t.Error("expected error, got nil")
				}
			},
		},
		{
			name: "ClearUnknown",
			fn: func(t *testing.T, s *Store) {
				if err := s.ClearUnknown(context.Background(), "telegram", "id"); err == nil {
					t.Error("expected error, got nil")
				}
			},
		},
		{
			name: "LinkAndClearUnknown",
			fn: func(t *testing.T, s *Store) {
				if err := s.LinkAndClearUnknown(context.Background(), "user", "telegram", "id"); err == nil {
					t.Error("expected error, got nil")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			s := NewStore(db)
			tt.fn(t, s)
		})
	}
}

// Prevent unused import error for os
var _ = os.DevNull
