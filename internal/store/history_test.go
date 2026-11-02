package store

import (
	"reflect"
	"testing"
)

// seedConversationWithTiedMessages inserts a conversation plus the given
// message ids, all carrying the SAME created_at value, physically inserted in
// `insertOrder` (so insertion order deliberately does not match id order).
// This is the scenario the id tiebreaker in GetConversationHistory was added to
// keep deterministic: rows sharing one timestamp.
func seedConversationWithTiedMessages(t *testing.T, db *DB, convID string, insertOrder []int64) {
	t.Helper()
	raw := db.SQL()
	if _, err := raw.Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, convID, "alice"); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	for _, id := range insertOrder {
		if _, err := raw.Exec(`INSERT INTO messages (id, conversation_id, role, content, category, policy_action, gateway, created_at)
			VALUES (?, ?, 'user', ?, '', '', 'web', '2026-01-01 00:00:00')`,
			id, convID, "tied-message"); err != nil {
			t.Fatalf("insert message id=%d: %v", id, err)
		}
	}
}

func TestGetConversationHistoryDeterministicOnCreatedAtTie(t *testing.T) {
	// Contract under test: when several messages in a conversation share the
	// same created_at, GetConversationHistory must order them deterministically
	// by id — the larger id is the later message. The returned slice is
	// chronological (oldest first), so tied ids come back ascending. Without
	// the `id DESC` tiebreaker in the query, the tie order is left to the
	// engine and this test fails (empirically it returns the ids descending).
	cases := []struct {
		name        string
		insertOrder []int64 // ids, in physical insertion order (shuffled on purpose)
		wantIDs     []int64 // expected returned (chronological) id order
	}{
		{
			name:        "all-tied: larger id is the later message (ascending in returned slice)",
			insertOrder: []int64{3, 1, 2},
			wantIDs:     []int64{1, 2, 3},
		},
		{
			name:        "wider tie: five tied ids come back ascending regardless of insertion order",
			insertOrder: []int64{9, 4, 2, 7, 1},
			wantIDs:     []int64{1, 2, 4, 7, 9},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()

			convID := "history-tie"
			seedConversationWithTiedMessages(t, db, convID, tc.insertOrder)

			msgs, err := db.GetConversationHistory(convID, len(tc.wantIDs))
			if err != nil {
				t.Fatalf("GetConversationHistory: %v", err)
			}
			if len(msgs) != len(tc.wantIDs) {
				t.Fatalf("expected %d messages, got %d", len(tc.wantIDs), len(msgs))
			}
			var got []int64
			for _, m := range msgs {
				got = append(got, m.ID)
			}
			if !reflect.DeepEqual(got, tc.wantIDs) {
				t.Errorf("returned id order = %v, want %v (deterministic id tiebreaker on equal created_at)", got, tc.wantIDs)
			}
		})
	}
}

// TestGetConversationHistoryIDBreaksTiesOnly guards the complementary property:
// the id tiebreaker must only decide order among equal timestamps — a strictly
// later created_at must always sort after an earlier one even when its id is
// smaller. This prevents the tiebreaker from masquerading as the primary sort.
func TestGetConversationHistoryIDBreaksTiesOnly(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	convID := "history-mixed"
	raw := db.SQL()
	if _, err := raw.Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, convID, "alice"); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	// id 1 is earliest; ids 2 and 5 tie on day-2 (5 is the later of the two);
	// id 9 is latest. Inserted out of id order to rule out scan-order luck.
	seed := []struct {
		id   int64
		at   string
		role string
	}{
		{5, "2026-01-02 00:00:00", "user"},
		{2, "2026-01-02 00:00:00", "user"},
		{9, "2026-01-03 00:00:00", "user"},
		{1, "2026-01-01 00:00:00", "user"},
	}
	for _, s := range seed {
		if _, err := raw.Exec(`INSERT INTO messages (id, conversation_id, role, content, category, policy_action, gateway, created_at)
			VALUES (?, ?, ?, ?, '', '', 'web', ?)`,
			s.id, convID, s.role, "mixed", s.at); err != nil {
			t.Fatalf("insert message id=%d: %v", s.id, err)
		}
	}

	msgs, err := db.GetConversationHistory(convID, 10)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	var got []int64
	for _, m := range msgs {
		got = append(got, m.ID)
	}
	want := []int64{1, 2, 5, 9} // chronological: day1; day2 tie -> id2 before id5; day3
	if !reflect.DeepEqual(got, want) {
		t.Errorf("returned id order = %v, want %v (created_at primary, id only breaks ties)", got, want)
	}
}
