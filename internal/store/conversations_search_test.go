package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// seedMessage inserts a message with an explicit created_at so date-range
// tests are deterministic. The insert goes through the production schema
// (triggers included when present), so the FTS index sees it exactly like
// a SaveMessage write.
func seedMessage(t *testing.T, db *DB, convID, userName, role, content, createdAt string) {
	t.Helper()
	if _, err := db.sql.Exec(`INSERT OR IGNORE INTO conversations (id, user_name) VALUES (?, ?)`, convID, userName); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO messages (conversation_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		convID, role, content, createdAt); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func countMessages(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

// ftsRowCount returns the number of rows indexed in messages_fts.
func ftsRowCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&n); err != nil {
		t.Fatalf("count messages_fts: %v", err)
	}
	return n
}

func assertFTSPresent(t *testing.T, db *DB) {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='messages_fts'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 messages_fts table, got %d", n)
	}
	for _, trig := range []string{"messages_ai", "messages_ad", "messages_au"} {
		var c int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trig).Scan(&c); err != nil {
			t.Fatalf("query trigger %s: %v", trig, err)
		}
		if c != 1 {
			t.Errorf("expected trigger %s present exactly once, got %d", trig, c)
		}
	}
}

func TestSearchConversationsMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()

	// First Open creates messages_fts + triggers and runs the one-shot
	// backfill on an empty corpus.
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("open #1: %v", err)
	}
	assertFTSPresent(t, db1)

	// Simulate a pre-trigger corpus: drop the FTS index and triggers, then
	// insert rows that no trigger can see.
	if _, err := db1.sql.Exec(`DROP TRIGGER IF EXISTS messages_ai; DROP TRIGGER IF EXISTS messages_ad; DROP TRIGGER IF EXISTS messages_au; DROP TABLE IF EXISTS messages_fts`); err != nil {
		t.Fatalf("drop fts: %v", err)
	}
	seedMessage(t, db1, "conv-a", "alice", "user", "pre-trigger notes about the garden", "2026-10-01 09:00:00")
	seedMessage(t, db1, "conv-a", "alice", "assistant", "planted the garden herbs today", "2026-10-01 09:01:00")
	_ = db1.Close()

	// Second Open finds no messages_fts -> recreates it and backfills the
	// pre-trigger rows via 'rebuild'.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("open #2: %v", err)
	}
	assertFTSPresent(t, db2)
	hits, err := db2.SearchConversations(ctx, "alice", "garden", "", "", 10)
	if err != nil {
		t.Fatalf("search after backfill: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 backfilled hits, got %d", len(hits))
	}
	if ftsRowCount(t, db2) != countMessages(t, db2) {
		t.Errorf("fts rows (%d) != message rows (%d)", ftsRowCount(t, db2), countMessages(t, db2))
	}
	_ = db2.Close()

	// Third Open: messages_fts already exists -> no re-backfill. Prove it
	// by dropping the triggers, inserting an unindexed row, reopening, and
	// showing that row was NOT picked up (a rebuild would have indexed it).
	db3, err := Open(path)
	if err != nil {
		t.Fatalf("open #3: %v", err)
	}
	assertFTSPresent(t, db3)
	if _, err := db3.sql.Exec(`DROP TRIGGER messages_ai; DROP TRIGGER messages_ad; DROP TRIGGER messages_au`); err != nil {
		t.Fatalf("drop triggers: %v", err)
	}
	seedMessage(t, db3, "conv-b", "alice", "user", "zesty unindexed zebra", "2026-10-02 09:00:00")
	_ = db3.Close()

	db4, err := Open(path)
	if err != nil {
		t.Fatalf("open #4: %v", err)
	}
	assertFTSPresent(t, db4) // triggers reinstalled, table untouched
	hits, err = db4.SearchConversations(ctx, "alice", "zebra", "", "", 10)
	if err != nil {
		t.Fatalf("search unindexed row: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits for unindexed row after non-creating Open, got %d (rebuild ran?)", len(hits))
	}
	// The backfilled rows are still searchable.
	hits, err = db4.SearchConversations(ctx, "alice", "garden", "", "", 10)
	if err != nil {
		t.Fatalf("search backfilled rows: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 backfilled hits to survive, got %d", len(hits))
	}
	_ = db4.Close()
}

func TestSearchConversationsSync(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// insert -> found
	if err := db.SaveMessage("conv-sync", "alice", "user", "shall we grab lunch at the ramen place", "", "", "web"); err != nil {
		t.Fatalf("save: %v", err)
	}
	hits, err := db.SearchConversations(ctx, "alice", "ramen", "", "", 10)
	if err != nil {
		t.Fatalf("search after insert: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit after insert, got %d", len(hits))
	}
	if !strings.Contains(hits[0].Snippet, "ramen") {
		t.Errorf("snippet missing matched term: %q", hits[0].Snippet)
	}

	// update -> new text found, old text not
	if _, err := db.sql.Exec(`UPDATE messages SET content=? WHERE content=?`,
		"changed the plan: dinner at the bistro instead", "shall we grab lunch at the ramen place"); err != nil {
		t.Fatalf("update: %v", err)
	}
	hits, err = db.SearchConversations(ctx, "alice", "bistro", "", "", 10)
	if err != nil {
		t.Fatalf("search after update: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit for updated text, got %d", len(hits))
	}
	hits, err = db.SearchConversations(ctx, "alice", "ramen", "", "", 10)
	if err != nil {
		t.Fatalf("search old text after update: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits for replaced text, got %d", len(hits))
	}

	// delete -> gone
	if _, err := db.sql.Exec(`DELETE FROM messages WHERE content='changed the plan: dinner at the bistro instead'`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	hits, err = db.SearchConversations(ctx, "alice", "bistro", "", "", 10)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits after delete, got %d", len(hits))
	}
}

func TestSearchConversationsUserScoping(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := db.SaveMessage("conv-alice", "alice", "user", "lunch plans with the team", "", "", "web"); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := db.SaveMessage("conv-bob", "bob", "user", "lunch at the secret sushi spot", "", "", "web"); err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	// alice cannot see bob's messages even with a matching query.
	hits, err := db.SearchConversations(ctx, "alice", "lunch", "", "", 10)
	if err != nil {
		t.Fatalf("search alice: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly alice's 1 hit, got %d", len(hits))
	}
	for _, h := range hits {
		if h.ConversationID != "conv-alice" {
			t.Fatalf("hit leaked into another conversation: %s", h.ConversationID)
		}
		if strings.Contains(h.Snippet, "sushi") {
			t.Fatalf("bob's message leaked into alice's results: %q", h.Snippet)
		}
	}

	// A query term that only exists in bob's corpus returns nothing for alice.
	hits, err = db.SearchConversations(ctx, "alice", "sushi", "", "", 10)
	if err != nil {
		t.Fatalf("search alice for sushi: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(hits))
	}

	// bob sees his own.
	hits, err = db.SearchConversations(ctx, "bob", "sushi", "", "", 10)
	if err != nil {
		t.Fatalf("search bob: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected bob's 1 hit, got %d", len(hits))
	}
}

func TestSearchConversationsDateRange(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	seedMessage(t, db, "conv-d", "alice", "user", "early meeting notes", "2026-10-01 08:00:00")
	seedMessage(t, db, "conv-d", "alice", "user", "midweek lunch notes", "2026-10-12 12:00:00")
	seedMessage(t, db, "conv-d", "alice", "user", "late project notes", "2026-10-20 18:00:00")

	cases := []struct {
		name     string
		from, to string
		want     int
	}{
		{"no bounds", "", "", 3},
		{"mid band", "2026-10-05", "2026-10-15", 1},
		{"lower bound", "2026-10-13", "", 1},
		{"upper bound", "", "2026-10-01", 1},
		{"inclusive lower edge", "2026-10-12", "2026-10-12", 1},
		{"gap band", "2026-10-03", "2026-10-11", 0},
		{"invalid from ignored", "not-a-date", "2026-10-01", 1},
		{"invalid to ignored", "2026-10-01", "99/99/9999", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := db.SearchConversations(ctx, "alice", "notes", tc.from, tc.to, 10)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(hits) != tc.want {
				t.Fatalf("expected %d hits, got %d", tc.want, len(hits))
			}
		})
	}
}

func TestSearchConversationsMaxResultsCap(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		if err := db.SaveMessage("conv-cap", "alice", "user",
			fmt.Sprintf("widget observation number %d", i), "", "", "web"); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	cases := []struct {
		give, want int
	}{
		{0, 10}, // default
		{1, 1},
		{3, 3},
		{25, 25},
		{1000, 25}, // clamped to hard max
		{-4, 10},   // non-positive -> default
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("give_%d", tc.give), func(t *testing.T) {
			hits, err := db.SearchConversations(ctx, "alice", "widget", "", "", tc.give)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(hits) != tc.want {
				t.Fatalf("expected %d hits, got %d", tc.want, len(hits))
			}
		})
	}
}

func TestSearchConversationsQueryEscaping(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := db.SaveMessage("conv-q", "alice", "user",
		"we need foo, bar and baz, and also some or-words", "", "", "web"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.SaveMessage("conv-q", "alice", "user",
		"the exact phrase hello world is here", "", "", "web"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// FTS5 operator-laden query must be escaped to plain-term AND.
	hits, err := db.SearchConversations(ctx, "alice", `foo AND (bar OR *baz)`, "", "", 10)
	if err != nil {
		t.Fatalf("operator query surfaced an error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 escaped-hit, got %d", len(hits))
	}

	// Quoted exact phrase.
	hits, err = db.SearchConversations(ctx, "alice", `"hello world"`, "", "", 10)
	if err != nil {
		t.Fatalf("phrase query surfaced an error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 phrase hit, got %d", len(hits))
	}

	// Operator-only query: no usable tokens -> empty result, no error.
	hits, err = db.SearchConversations(ctx, "alice", `!!! *** ???`, "", "", 10)
	if err != nil {
		t.Fatalf("operator-only query surfaced an error: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits for operator-only query, got %d", len(hits))
	}

	// Empty query -> error (contract), not a silent empty search.
	if _, err = db.SearchConversations(ctx, "alice", "   ", "", "", 10); err == nil {
		t.Fatalf("expected error for blank query")
	}
	// Missing user -> error.
	if _, err = db.SearchConversations(ctx, "", "foo", "", "", 10); err == nil {
		t.Fatalf("expected error for empty user")
	}
}

func TestFts5MatchExpr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"lunch", `"lunch"`},
		{"ramen place", `"ramen" AND "place"`},
		{`"ramen place"`, `"ramen place"`},
		{`foo* -bar ^baz (qux)`, `"foo" AND "bar" AND "baz" AND "qux"`},
		{`!!!`, ""},
		{`mother-in-law`, `"mother-in-law"`},
		{`don't`, `"don't"`},
		{`"exact phrase" extra`, `"exact phrase" AND "extra"`},
		{`unbalanced "quote here`, `"unbalanced" AND "quote" AND "here"`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := fts5MatchExpr(tc.in); got != tc.want {
				t.Fatalf("fts5MatchExpr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
