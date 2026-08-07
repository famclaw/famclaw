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

	// Continuation cases chain: id2 reuses id1 as its lastConvID so the
	// stored ID is reused verbatim within the idle window. (The old
	// derivation re-seeded from the last message's timestamp, which
	// advanced on every message and broke stability.)
	midnightID := ConversationID(userA, time.Time{}, false, "", preMidnight)
	sameDayID := ConversationID(userA, time.Time{}, false, "", tenAM)

	tests := []struct {
		name     string
		id1      string
		id2      string
		wantSame bool
	}{
		{
			name:     "midnight gap under 6h continues conversation",
			id1:      midnightID,
			id2:      ConversationID(userA, preMidnight, true, midnightID, postMidnight),
			wantSame: true,
		},
		{
			name:     "7h gap starts new conversation",
			id1:      ConversationID(userA, postMidnight, true, "", postMidnight),
			id2:      ConversationID(userA, postMidnight, true, "", sevenHoursLater),
			wantSame: false,
		},
		{
			name:     "5min gap continues conversation",
			id1:      sameDayID,
			id2:      ConversationID(userA, tenAM, true, sameDayID, fiveMinLaterSameDay),
			wantSame: true,
		},
		{
			name:     "different users never share id",
			id1:      ConversationID(userA, preMidnight, true, "", postMidnight),
			id2:      ConversationID(userB, preMidnight, true, "", postMidnight),
			wantSame: false,
		},
		{
			name:     "cold start produces fresh id",
			id1:      ConversationID(userA, time.Time{}, false, "", preMidnight),
			id2:      ConversationID(userA, time.Time{}, false, "", postMidnight),
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

// TestConversationIDStableAcrossMessages is the regression test for the
// idle-gap derivation: three user messages a few seconds apart must all
// share ONE conversation id. On the unfixed code (which re-seeded from the
// last message's advancing timestamp) this fails on the third message.
func TestConversationIDStableAcrossMessages(t *testing.T) {
	cases := []struct {
		name  string
		times []time.Time
	}{
		{
			name: "three messages 5s apart share one id",
			times: []time.Time{
				time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				time.Date(2024, 1, 15, 10, 0, 5, 0, time.UTC),
				time.Date(2024, 1, 15, 10, 0, 10, 0, time.UTC),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()
			ctx := context.Background()
			user := "emma"

			ids := make([]string, len(tc.times))
			for i, now := range tc.times {
				convID, lastMsg, hasLast, err := db.LastMessage(ctx, user)
				if err != nil {
					t.Fatalf("LastMessage: %v", err)
				}
				ids[i] = ConversationID(user, lastMsg, hasLast, convID, now)
				if _, err := db.SQL().Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, ids[i], user); err != nil {
					t.Fatalf("insert conversation: %v", err)
				}
				if _, err := db.SQL().Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES (?, 'user', 'msg', 'safe', 'allow', ?)`, ids[i], now.Format("2006-01-02 15:04:05")); err != nil {
					t.Fatalf("insert message: %v", err)
				}
			}
			for i := 1; i < len(ids); i++ {
				if ids[i] != ids[0] {
					t.Errorf("message %d convID = %q, want %q (stable across messages)", i+1, ids[i], ids[0])
				}
			}
		})
	}
}

// TestConversationIDTimeoutStartsNewConversation pins the other half of the
// contract: once ConversationIdleTimeout has elapsed, the next message MUST
// start a new conversation.
func TestConversationIDTimeoutStartsNewConversation(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	user := "emma"

	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	// Cold start: no prior message.
	convID, lastMsg, hasLast, err := db.LastMessage(ctx, user)
	if err != nil {
		t.Fatalf("LastMessage (cold): %v", err)
	}
	if hasLast {
		t.Fatalf("expected cold start, got hasLast=true lastMsg=%v", lastMsg)
	}
	id1 := ConversationID(user, lastMsg, hasLast, convID, now)
	if _, err := db.SQL().Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, id1, user); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO messages (conversation_id, role, content, category, policy_action, created_at) VALUES (?, 'user', 'msg', 'safe', 'allow', ?)`, id1, now.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	// After the idle timeout has elapsed, the next message starts fresh.
	now2 := now.Add(ConversationIdleTimeout + time.Second)
	convID2, lastMsg2, hasLast2, err := db.LastMessage(ctx, user)
	if err != nil {
		t.Fatalf("LastMessage (warm): %v", err)
	}
	if !hasLast2 {
		t.Fatalf("expected prior message, got cold start")
	}
	id2 := ConversationID(user, lastMsg2, hasLast2, convID2, now2)
	if id1 == id2 {
		t.Errorf("expected new conversation id after idle timeout, both %q", id1)
	}
}

func TestLastMessage(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*DB) error
		userName string
		wantOk   bool
		wantTime time.Time // only checked when wantOk is true
		wantConv string    // only checked when wantOk is true
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
			wantConv: "conv-abc",
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
			// Pins the cross-conversation semantics: LastMessage
			// returns the most recent message across ALL of the user's
			// conversations, including its conversation ID, not just
			// one. This is intentional — see the doc comment.
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
			wantConv: "conv-b",
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
			wantConv: "conv-x",
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
			gotConv, got, ok, err := db.LastMessage(ctx, tc.userName)
			if err != nil {
				t.Fatalf("LastMessage: %v", err)
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
			if tc.wantOk && tc.wantConv != "" && gotConv != tc.wantConv {
				t.Errorf("convID = %q, want %q", gotConv, tc.wantConv)
			}
		})
	}
}
