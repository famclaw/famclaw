package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationGatewayColumn verifies that migrating a database created at the
// PREVIOUS schema version (messages table without a gateway column) applies
// cleanly and that existing rows survive with the 'unknown' sentinel backfill.
func TestMigrationGatewayColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// ── Recreate the previous schema (no gateway column) ──────────────────
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE conversations (
			id          TEXT PRIMARY KEY,
			user_name   TEXT NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE messages (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL REFERENCES conversations(id),
			role            TEXT NOT NULL,
			content         TEXT NOT NULL,
			category        TEXT,
			policy_action   TEXT,
			created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_messages_conv ON messages(conversation_id);
	`); err != nil {
		raw.Close()
		t.Fatalf("create old schema: %v", err)
	}

	// Insert existing data that must survive the migration intact.
	if _, err := raw.Exec(`
		INSERT INTO conversations (id, user_name) VALUES ('conv-1', 'alice');
		INSERT INTO messages (conversation_id, role, content, category, policy_action)
		VALUES ('conv-1', 'user', 'hello world', 'safe', 'allow');
	`); err != nil {
		raw.Close()
		t.Fatalf("insert old data: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// ── Open with store.Open — migrate() must add the gateway column ───────
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Existing rows must survive and backfill to 'unknown'.
	msgs, err := db.GetConversationHistory("conv-1", 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}
	if got, want := len(msgs), 1; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
	m := msgs[0]
	if m.Content != "hello world" {
		t.Errorf("content = %q, want %q (row content must survive)", m.Content, "hello world")
	}
	if m.Role != "user" {
		t.Errorf("role = %q, want %q", m.Role, "user")
	}
	if m.Category != "safe" {
		t.Errorf("category = %q, want %q", m.Category, "safe")
	}
	if m.PolicyAction != "allow" {
		t.Errorf("policy_action = %q, want %q", m.PolicyAction, "allow")
	}
	if m.Gateway != "unknown" {
		t.Errorf("gateway = %q, want %q (backfill sentinel)", m.Gateway, "unknown")
	}

	// New messages via SaveMessage must persist the gateway explicitly.
	if err := db.SaveMessage("conv-1", "alice", "assistant", "hi back", "", "", "telegram"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	msgs, err = db.GetConversationHistory("conv-1", 20)
	if err != nil {
		t.Fatalf("GetConversationHistory after save: %v", err)
	}
	if got, want := len(msgs), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
	// Existing rows backfilled to 'unknown'; new rows use the explicit gateway.
	// Verify by content (created_at granularity is 1s, so same-timestamp
	// rows may not be in a deterministic position after the reverse).
	for _, m := range msgs {
		switch m.Content {
		case "hello world":
			if m.Gateway != "unknown" {
				t.Errorf("migrated message gateway = %q, want %q", m.Gateway, "unknown")
			}
		case "hi back":
			if m.Gateway != "telegram" {
				t.Errorf("new message gateway = %q, want %q", m.Gateway, "telegram")
			}
		}
	}
}

// TestSaveMessageGateway verifies that a message saved with a specific gateway
// reads back with that same gateway. Covers the Telegram and Discord paths
// explicitly (table-driven) plus additional gateways for completeness.
func TestSaveMessageGateway(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
	}{
		{name: "telegram", gateway: "telegram"},
		{name: "discord", gateway: "discord"},
		{name: "whatsapp", gateway: "whatsapp"},
		{name: "web", gateway: "web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()

			convID := "conv-" + tc.gateway
			if err := db.SaveMessage(convID, "alice", "user", "hi", "", "", tc.gateway); err != nil {
				t.Fatalf("SaveMessage: %v", err)
			}

			msgs, err := db.GetConversationHistory(convID, 20)
			if err != nil {
				t.Fatalf("GetConversationHistory: %v", err)
			}
			if got, want := len(msgs), 1; got != want {
				t.Fatalf("expected %d message, got %d", want, got)
			}
			if msgs[0].Gateway != tc.gateway {
				t.Errorf("gateway = %q, want %q", msgs[0].Gateway, tc.gateway)
			}
			if msgs[0].Content != "hi" {
				t.Errorf("content = %q, want %q", msgs[0].Content, "hi")
			}
			if msgs[0].Role != "user" {
				t.Errorf("role = %q, want %q", msgs[0].Role, "user")
			}
		})
	}
}

// TestMostRecentGatewayForUser verifies that the most-recent-gateway query
// returns the latest gateway for a user with messages on multiple platforms,
// and returns an empty string for users with no messages.
func TestMostRecentGatewayForUser(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	// Save a message on Telegram, then on Discord — Discord is later.
	if err := db.SaveMessage("conv-telegram", "alice", "user", "msg1", "", "", "telegram"); err != nil {
		t.Fatalf("SaveMessage telegram: %v", err)
	}
	// Ensure the Discord message has a strictly later created_at timestamp
	// (CURRENT_TIMESTAMP has second-level granularity).
	time.Sleep(1100 * time.Millisecond)
	if err := db.SaveMessage("conv-discord", "alice", "user", "msg2", "", "", "discord"); err != nil {
		t.Fatalf("SaveMessage discord: %v", err)
	}

	gw, err := db.MostRecentGatewayForUser("alice")
	if err != nil {
		t.Fatalf("MostRecentGatewayForUser: %v", err)
	}
	if gw != "discord" {
		t.Errorf("most recent gateway = %q, want %q", gw, "discord")
	}

	// A user with no messages gets an empty string (not an error).
	gw, err = db.MostRecentGatewayForUser("bob")
	if err != nil {
		t.Fatalf("MostRecentGatewayForUser bob: %v", err)
	}
	if gw != "" {
		t.Errorf("most recent gateway for no-message user = %q, want empty", gw)
	}
}

// TestMostRecentGatewayAndExternalIDForUser verifies that the
// most-recent-gateway-and-external_id query returns the latest gateway and
// the linked external_id for a user, and returns empty strings for users
// with no messages.
func TestMostRecentGatewayAndExternalIDForUser(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// alice has a Telegram message and a linked Telegram account.
	if err := db.SaveMessage("conv-tg-alice", "alice", "user", "hello", "", "", "telegram"); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if err := db.LinkGatewayAccount("alice", "telegram", "12345"); err != nil {
		t.Fatalf("LinkGatewayAccount: %v", err)
	}

	gw, extID, err := db.MostRecentGatewayAndExternalIDForUser(ctx, "alice")
	if err != nil {
		t.Fatalf("MostRecentGatewayAndExternalIDForUser: %v", err)
	}
	if gw != "telegram" {
		t.Errorf("gateway = %q, want %q", gw, "telegram")
	}
	if extID != "12345" {
		t.Errorf("external_id = %q, want %q", extID, "12345")
	}

	// bob has Discord messages + account, then later Telegram messages + account.
	if err := db.SaveMessage("conv-disc-bob", "bob", "user", "hi", "", "", "discord"); err != nil {
		t.Fatalf("SaveMessage discord: %v", err)
	}
	if err := db.LinkGatewayAccount("bob", "discord", "d-999"); err != nil {
		t.Fatalf("LinkGatewayAccount discord: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := db.SaveMessage("conv-tg-bob", "bob", "user", "hello", "", "", "telegram"); err != nil {
		t.Fatalf("SaveMessage telegram: %v", err)
	}
	if err := db.LinkGatewayAccount("bob", "telegram", "t-555"); err != nil {
		t.Fatalf("LinkGatewayAccount telegram: %v", err)
	}

	gw, extID, err = db.MostRecentGatewayAndExternalIDForUser(ctx, "bob")
	if err != nil {
		t.Fatalf("MostRecentGatewayAndExternalIDForUser bob: %v", err)
	}
	if gw != "telegram" {
		t.Errorf("gateway = %q, want %q (most recent)", gw, "telegram")
	}
	if extID != "t-555" {
		t.Errorf("external_id = %q, want %q", extID, "t-555")
	}

	// User with no messages gets empty strings, no error.
	gw, extID, err = db.MostRecentGatewayAndExternalIDForUser(ctx, "ghost")
	if err != nil {
		t.Fatalf("MostRecentGatewayAndExternalIDForUser ghost: %v", err)
	}
	if gw != "" || extID != "" {
		t.Errorf("ghost: want empty gateway/external_id, got gw=%q ext=%q", gw, extID)
	}

	// carol has sent a message but has NO linked gateway account. With the
	// gateway_accounts-authoritative resolution, she is unreachable: the
	// destination can only come from a linked account. The caller treats empty
	// gateway/external_id as "no reachable destination."
	if err := db.SaveMessage("conv-tg-nolink", "carol", "user", "hi", "", "", "telegram"); err != nil {
		t.Fatalf("SaveMessage carol: %v", err)
	}
	gw, extID, err = db.MostRecentGatewayAndExternalIDForUser(ctx, "carol")
	if err != nil {
		t.Fatalf("MostRecentGatewayAndExternalIDForUser carol: %v", err)
	}
	if gw != "" || extID != "" {
		t.Errorf("carol: want empty gateway/external_id (message without a linked account is unreachable), got gw=%q ext=%q", gw, extID)
	}

	// julia is linked on telegram + discord but has NEVER sent a message.
	// Resolution must come from gateway_accounts (the authoritative reach
	// record), not from messages — a linked-but-silent user must always
	// be reachable. This case FAILS on the old messages-only query.
	if err := db.LinkGatewayAccount("julia", "telegram", "julia-tg"); err != nil {
		t.Fatalf("LinkGatewayAccount julia telegram: %v", err)
	}
	if err := db.LinkGatewayAccount("julia", "discord", "julia-disc"); err != nil {
		t.Fatalf("LinkGatewayAccount julia discord: %v", err)
	}
	gw, extID, err = db.MostRecentGatewayAndExternalIDForUser(ctx, "julia")
	if err != nil {
		t.Fatalf("MostRecentGatewayAndExternalIDForUser julia (linked, no messages): %v", err)
	}
	if gw == "" || extID == "" {
		t.Errorf("julia (linked-but-silent): got empty destination gw=%q ext=%q; want a usable gateway+external_id from gateway_accounts", gw, extID)
	}
}
