package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestApprovalJSONSerialization(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*testing.T)
	}{
		{
			name: "marshals to lowercase JSON keys (not PascalCase)",
			fn: func(t *testing.T) {
				approval := &Approval{
					ID:           "abc123",
					UserName:     "alice",
					UserDisplay:  "Alice Smith",
					AgeGroup:     "teen",
					Category:     "medical",
					QueryText:    "Can I take ibuprofen?",
					Status:       "pending",
					CreatedAt:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
					UpdatedAt:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
					ExpiresAt:    time.Date(2024, 1, 16, 10, 30, 0, 0, time.UTC),
					DecidedBy:    "",
					DecisionNote: "",
				}

				data, err := json.Marshal(approval)
				if err != nil {
					t.Fatalf("json.Marshal: %v", err)
				}
				jsonStr := string(data)

				// Must contain lowercase JSON keys (the fix adds json:"id" etc tags)
				expectedKeys := []string{
					`"id":"abc123"`,
					`"user_name":"alice"`,
					`"user_display":"Alice Smith"`,
					`"age_group":"teen"`,
					`"category":"medical"`,
					`"query_text":"Can I take ibuprofen?"`,
					`"status":"pending"`,
					`"decided_by":""`,
					`"decision_note":""`,
				}
				for _, key := range expectedKeys {
					if !strings.Contains(jsonStr, key) {
						t.Errorf("JSON missing expected key %q in output: %s", key, jsonStr)
					}
				}

				// Must NOT contain PascalCase keys (would happen without json tags)
				forbiddenKeys := []string{`"ID"`, `"UserName"`, `"UserDisplay"`, `"AgeGroup"`, `"Category"`, `"QueryText"`, `"Status"`, `"DecidedBy"`, `"DecisionNote"`}
				for _, key := range forbiddenKeys {
					if strings.Contains(jsonStr, key) {
						t.Errorf("JSON contains forbidden PascalCase key %q in output: %s", key, jsonStr)
					}
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.fn)
	}
}

func TestConversationID(t *testing.T) {
	// Fixed timestamps for deterministic, table-driven tests.
	// Day boundary: Jan 15 23:58 → Jan 16 00:02 (2 minutes apart, straddle midnight).
	preMidnight := time.Date(2024, 1, 15, 23, 58, 0, 0, time.UTC)
	postMidnight := time.Date(2024, 1, 16, 0, 2, 0, 0, time.UTC)
	sevenHoursLater := postMidnight.Add(7 * time.Hour)
	// Same-day 5-minute gap for a clean 'continue' test (not straddling midnight).
	tenAM := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	fiveMinLaterSameDay := tenAM.Add(5 * time.Minute)

	userA := "emma"
	userB := "lucas"

	tests := []struct {
		name     string
		id1      string
		id2      string
		wantSame bool
	}{
		{
			name:     "midnight gap under 6h continues conversation",
			id1:      ConversationID(userA, time.Time{}, false, preMidnight),
			id2:      ConversationID(userA, preMidnight, true, postMidnight),
			wantSame: true,
		},
		{
			name:     "7h gap starts new conversation",
			id1:      ConversationID(userA, postMidnight, true, postMidnight),
			id2:      ConversationID(userA, postMidnight, true, sevenHoursLater),
			wantSame: false,
		},
		{
			name:     "5min gap continues conversation",
			id1:      ConversationID(userA, time.Time{}, false, tenAM),
			id2:      ConversationID(userA, tenAM, true, fiveMinLaterSameDay),
			wantSame: true,
		},
		{
			name:     "different users never share id",
			id1:      ConversationID(userA, preMidnight, true, postMidnight),
			id2:      ConversationID(userB, preMidnight, true, postMidnight),
			wantSame: false,
		},
		{
			name:     "cold start produces fresh id",
			id1:      ConversationID(userA, time.Time{}, false, preMidnight),
			id2:      ConversationID(userA, time.Time{}, false, postMidnight),
			wantSame: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantSame {
				if tc.id1 != tc.id2 {
					t.Errorf("expected same conversation id, got %q and %q", tc.id1, tc.id2)
				}
			} else {
				if tc.id1 == tc.id2 {
					t.Errorf("expected different conversation ids, both %q", tc.id1)
				}
			}
		})
	}
}

func TestLastMessageTime(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*DB) error
		userName string
		wantOk   bool
		wantTime time.Time // only checked when wantOk is true
	}{
		{
			name:     "cold start no messages",
			setup:    func(db *DB) error { return nil },
			userName: "emma",
			wantOk:   false,
		},
		{
			name: "has prior message",
			setup: func(db *DB) error {
				return db.SaveMessage("conv-abc", "emma", "user", "hello", "safe", "allow", "unknown")
			},
			userName: "emma",
			wantOk:   true,
		},
		{
			name: "different user no messages",
			setup: func(db *DB) error {
				return db.SaveMessage("conv-abc", "emma", "user", "hello", "safe", "allow", "unknown")
			},
			userName: "lucas",
			wantOk:   false,
		},
		{
			// Pins the cross-conversation semantics: LastMessageTime
			// returns the most recent message across ALL of the user's
			// conversations, not just one. This is intentional — see the
			// doc comment on LastMessageTime.
			name: "returns most recent across all conversations",
			setup: func(db *DB) error {
				sql := db.SQL()
				if _, err := sql.Exec(`INSERT INTO conversations (id, user_name) VALUES ('conv-a', 'emma')`); err != nil {
					return err
				}
				if _, err := sql.Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES ('conv-a', 'user', 'msg A', 'safe', 'allow', '2024-01-15 10:00:00')`); err != nil {
					return err
				}
				if _, err := sql.Exec(`INSERT INTO conversations (id, user_name) VALUES ('conv-b', 'emma')`); err != nil {
					return err
				}
				_, err := sql.Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES ('conv-b', 'user', 'msg B', 'safe', 'allow', '2024-01-15 11:00:00')`)
				return err
			},
			userName: "emma",
			wantOk:   true,
			wantTime: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
		},
		{
			// Pins the role='user' filter: only user-initiated
			// messages are conversation boundaries. An assistant
			// reply at 11:00 must NOT reset the idle timer over a
			// user message at 10:00.
			name: "ignores assistant messages",
			setup: func(db *DB) error {
				sql := db.SQL()
				if _, err := sql.Exec(`INSERT INTO conversations (id, user_name) VALUES ('conv-x', 'emma')`); err != nil {
					return err
				}
				if _, err := sql.Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES ('conv-x', 'user', 'user msg', 'safe', 'allow', '2024-01-15 10:00:00')`); err != nil {
					return err
				}
				_, err := sql.Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES ('conv-x', 'assistant', 'bot reply', 'safe', 'allow', '2024-01-15 11:00:00')`)
				return err
			},
			userName: "emma",
			wantOk:   true,
			wantTime: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()
			ctx := context.Background()
			if err := tc.setup(db); err != nil {
				t.Fatalf("setup: %v", err)
			}
			got, ok, err := db.LastMessageTime(ctx, tc.userName)
			if err != nil {
				t.Fatalf("LastMessageTime: %v", err)
			}
			if ok != tc.wantOk {
				t.Errorf("ok = %v, want %v", ok, tc.wantOk)
			}
			if tc.wantOk && got.IsZero() {
				t.Errorf("expected non-zero timestamp, got zero")
			}
			if tc.wantOk && !tc.wantTime.IsZero() && !got.Equal(tc.wantTime) {
				t.Errorf("timestamp = %v, want %v", got, tc.wantTime)
			}
		})
	}
}
