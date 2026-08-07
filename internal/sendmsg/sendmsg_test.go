package sendmsg

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
)

// --- test doubles ---

type mockSender struct {
	mu     sync.Mutex
	sent   []struct{ chatID, text string }
	failOn int // 0 = never fail; n>0 fails on the nth call
	callN  int
}

func (m *mockSender) Send(ctx context.Context, chatID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callN++
	m.sent = append(m.sent, struct{ chatID, text string }{chatID, text})
	if m.failOn > 0 && m.callN == m.failOn {
		return errMockSend
	}
	return nil
}

func (m *mockSender) getSent() []struct{ chatID, text string } {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent
}

var errMockSend = errMock("mock send failure")

type errMock string

func (e errMock) Error() string { return string(e) }

// --- test helpers ---

func newTestDB(t *testing.T) (*store.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}

func linkUser(t *testing.T, db *store.DB, userName, gw, externalID string) {
	t.Helper()
	convID := "conv-" + userName
	if err := db.SaveMessage(convID, userName, "user", "hi", "", "", gw); err != nil {
		t.Fatalf("SaveMessage for %s: %v", userName, err)
	}
	if err := db.LinkGatewayAccount(userName, gw, externalID); err != nil {
		t.Fatalf("LinkGatewayAccount for %s: %v", userName, err)
	}
}

func parentCfg() *config.Config {
	return &config.Config{
		Users: []config.UserConfig{
			{Name: "mom", Role: "parent", AgeGroup: ""},
			{Name: "julia", Role: "child", AgeGroup: "age_13_17"},
		},
	}
}

// --- tests ---

func TestTool(t *testing.T) {
	tc := Tool()
	if tc.Name != ToolName {
		t.Errorf("Name = %q, want %q", tc.Name, ToolName)
	}
	if tc.Source != "builtin" {
		t.Errorf("Source = %q, want builtin", tc.Source)
	}
	req, ok := tc.InputSchema["required"].([]string)
	if !ok || len(req) != 2 {
		t.Fatalf("expected 2 required fields, got %v", tc.InputSchema["required"])
	}
}

func TestHandle(t *testing.T) {
	cases := []struct {
		name       string
		to         string
		message    string
		setupDB    func(t *testing.T, db *store.DB)
		wantErr    bool
		wantErrSub string
	}{
		{
			name:    "deliver to telegram user",
			to:      "julia",
			message: "hi from mom",
			setupDB: func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr: false,
		},
		{
			name:    "deliver to discord user",
			to:      "julia",
			message: "hello via discord",
			setupDB: func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "discord", "julia-disc") },
			wantErr: false,
		},
		{
			name:       "unknown target",
			to:         "stranger",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) {},
			wantErr:    true,
			wantErrSub: "is not a configured family member",
		},
		{
			name:       "target with no gateway",
			to:         "julia",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) {},
			wantErr:    true,
			wantErrSub: "no linked gateway account",
		},
		{
			name:       "empty to",
			to:         "",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) {},
			wantErr:    true,
			wantErrSub: "requires both",
		},
		{
			name:       "empty message",
			to:         "julia",
			message:    "",
			setupDB:    func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:    true,
			wantErrSub: "requires both",
		},
		{
			name:       "gateway has no sender",
			to:         "julia",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:    true,
			wantErrSub: "no sender available for gateway",
		},
		{
			name:       "sender fails",
			to:         "julia",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:    true,
			wantErrSub: "mock send failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()
			ctx := context.Background()

			tc.setupDB(t, db)

			senders := map[string]gateway.Sender{}
			switch tc.name {
			case "sender fails":
				senders["telegram"] = &mockSender{failOn: 1}
			case "gateway has no sender":
				// whatsapp has no sender registered
			default:
				senders["telegram"] = &mockSender{}
				senders["discord"] = &mockSender{}
			}

			_, err := Handle(ctx, db, parentCfg(), senders, "mom", "telegram", tc.to, tc.message)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify delivery happened on the expected sender
			var found bool
			for _, s := range senders {
				if ms, ok := s.(*mockSender); ok {
					for _, sent := range ms.getSent() {
						if sent.text == tc.message {
							found = true
						}
					}
				}
			}
			if !found {
				t.Error("message was not recorded by any mock sender")
			}
		})
	}
}

// TestHandleResolvesTargetGateway verifies the message is delivered to the
// target's gateway, not the setter's.
func TestHandleResolvesTargetGateway(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "julia", "discord", "julia-disc")

	telegramSender := &mockSender{}
	discordSender := &mockSender{}
	senders := map[string]gateway.Sender{
		"telegram": telegramSender,
		"discord":  discordSender,
	}

	result, err := Handle(ctx, db, parentCfg(), senders, "mom", "telegram", "julia", "message to julia")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	sent := discordSender.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message to discord sender, got %d", len(sent))
	}
	if sent[0].chatID != "julia-disc" {
		t.Errorf("chatID = %q, want %q (julia's external_id)", sent[0].chatID, "julia-disc")
	}
	if sent[0].text != "message to julia" {
		t.Errorf("text = %q, want %q", sent[0].text, "message to julia")
	}
	if len(telegramSender.getSent()) != 0 {
		t.Error("telegram sender should not have been called")
	}
	if !strings.Contains(result, "discord") {
		t.Errorf("result should mention discord: %s", result)
	}
}

// TestHandleNoBroadcast verifies a single message goes to exactly one destination.
func TestHandleNoBroadcast(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "julia", "discord", "j-disc")
	time.Sleep(1100 * time.Millisecond)
	linkUser(t, db, "julia", "telegram", "j-tg")

	telegramSender := &mockSender{}
	discordSender := &mockSender{}
	senders := map[string]gateway.Sender{
		"telegram": telegramSender,
		"discord":  discordSender,
	}

	_, err := Handle(ctx, db, parentCfg(), senders, "mom", "telegram", "julia", "single message")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(telegramSender.getSent()) != 1 {
		t.Errorf("expected 1 message to telegram, got %d", len(telegramSender.getSent()))
	}
	if len(discordSender.getSent()) != 0 {
		t.Errorf("expected 0 messages to discord (no broadcast), got %d", len(discordSender.getSent()))
	}
}

// TestHandleAuditRecord verifies that a successful send writes an audit row
// recording who initiated it, the target, and the content.
func TestHandleAuditRecord(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "julia", "telegram", "julia-chat")

	senders := map[string]gateway.Sender{
		"telegram": &mockSender{},
	}

	msg := "remember to lock the door"
	result, err := Handle(ctx, db, parentCfg(), senders, "mom", "telegram", "julia", msg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Query the audit_log table for our entry.
	rows, err := db.SQL().QueryContext(ctx,
		`SELECT actor_name, gateway, tool_name, args FROM audit_log WHERE tool_name = ? ORDER BY id DESC LIMIT 1`,
		ToolName)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected at least one audit row, got none; result=%s", result)
	}

	var actor, gw, toolName, argsJSON string
	if err := rows.Scan(&actor, &gw, &toolName, &argsJSON); err != nil {
		t.Fatalf("scan audit row: %v", err)
	}

	if actor != "mom" {
		t.Errorf("actor = %q, want %q", actor, "mom")
	}
	if toolName != ToolName {
		t.Errorf("tool_name = %q, want %q", toolName, ToolName)
	}

	// The args JSON should contain the target and message.
	var args map[string]string
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("unmarshal audit args: %v", err)
	}
	if args["to"] != "julia" {
		t.Errorf("audit args.to = %q, want %q", args["to"], "julia")
	}
	if args["message"] != msg {
		t.Errorf("audit args.message = %q, want %q", args["message"], msg)
	}
}

// TestHandleSavesConversationHistory verifies the sent message appears in
// the target user's conversation history.
func TestHandleSavesConversationHistory(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "julia", "telegram", "julia-chat")

	senders := map[string]gateway.Sender{
		"telegram": &mockSender{},
	}

	msg := "proactive check-in"
	_, err := Handle(ctx, db, parentCfg(), senders, "mom", "telegram", "julia", msg)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Query the conversation that received the message.
	var convID string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT DISTINCT conversation_id FROM messages WHERE role = 'assistant' AND content = ? LIMIT 1`,
		msg).Scan(&convID); err != nil {
		t.Fatalf("query conversation for sent message: %v", err)
	}

	// Check the conversation's history contains our message.
	history, err := db.GetConversationHistory(convID, 20)
	if err != nil {
		t.Fatalf("GetConversationHistory: %v", err)
	}

	var found bool
	for _, m := range history {
		if m.Role == "assistant" && m.Content == msg {
			found = true
			break
		}
	}
	if !found {
		t.Error("sent message not found in conversation history")
	}
}

// TestResolveDestination covers each branch of the reachability resolution:
//   - a linked-but-never-messaged Telegram account is NOT initiable, while
//     Discord is always initiable → discord is chosen.
//   - Telegram becomes initiable once the user has a prior message.
//   - when nothing is initiable, the error names the person, lists the linked
//     gateway, explains why it can't be started, and gives the unblock step.
func TestResolveDestination(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, db *store.DB)
		wantGw  string
		wantExt string
		wantErr bool
		errSubs []string
	}{
		{
			name: "discord-only initiable: telegram linked but never messaged",
			setup: func(t *testing.T, db *store.DB) {
				// Link telegram WITHOUT saving a message → not initiable.
				if err := db.LinkGatewayAccount("julia", "telegram", "j-tg"); err != nil {
					t.Fatalf("LinkGatewayAccount: %v", err)
				}
				// Link discord → always initiable.
				if err := db.LinkGatewayAccount("julia", "discord", "j-disc"); err != nil {
					t.Fatalf("LinkGatewayAccount: %v", err)
				}
			},
			wantGw:  "discord",
			wantExt: "j-disc",
		},
		{
			name: "telegram initiable after a prior message",
			setup: func(t *testing.T, db *store.DB) {
				// linkUser saves a user message first → telegram becomes initiable.
				linkUser(t, db, "julia", "telegram", "j-tg")
			},
			wantGw:  "telegram",
			wantExt: "j-tg",
		},
		{
			name: "nothing initiable produces explanatory error",
			setup: func(t *testing.T, db *store.DB) {
				// Link telegram without a message and no discord → nothing initiable.
				if err := db.LinkGatewayAccount("julia", "telegram", "j-tg"); err != nil {
					t.Fatalf("LinkGatewayAccount: %v", err)
				}
			},
			wantErr: true,
			// Error must name the person, name the linked gateway, explain why,
			// and state what unblocks it.
			errSubs: []string{"julia", "telegram", "send one message", "Discord"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := newTestDB(t)
			defer cleanup()
			ctx := context.Background()

			tc.setup(t, db)

			gw, ext, err := ResolveDestination(ctx, db, "julia")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveDestination: expected error, got nil")
				}
				for _, sub := range tc.errSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Fatalf("error = %q, missing required substring %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveDestination: unexpected error: %v", err)
			}
			if gw != tc.wantGw {
				t.Errorf("gateway = %q, want %q", gw, tc.wantGw)
			}
			if ext != tc.wantExt {
				t.Errorf("externalID = %q, want %q", ext, tc.wantExt)
			}
		})
	}
}

// TestResolveDestinationPrefersMostRecentInitiable verifies that when several
// linked gateways are initiable, the most-recently-messaged one is chosen
// (preserving the ordering PR 332 established before initiability was layered
// in).
func TestResolveDestinationPrefersMostRecentInitiable(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	linkUser(t, db, "julia", "discord", "j-disc")
	time.Sleep(1100 * time.Millisecond)
	linkUser(t, db, "julia", "telegram", "j-tg")

	gw, ext, err := ResolveDestination(ctx, db, "julia")
	if err != nil {
		t.Fatalf("ResolveDestination: %v", err)
	}
	if gw != "telegram" || ext != "j-tg" {
		t.Errorf("wanted telegram/j-tg (most recent), got %s/%s", gw, ext)
	}
}

// TestResolveDestinationNoLinkedGateway verifies the honest "no linked gateway"
// error is still returned when the user has no gateway_accounts rows at all.
func TestResolveDestinationNoLinkedGateway(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	ctx := context.Background()

	_, _, err := ResolveDestination(ctx, db, "julia")
	if err == nil {
		t.Fatal("expected error for user with no linked gateway, got nil")
	}
	if !strings.Contains(err.Error(), "no linked gateway account") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "no linked gateway account")
	}
}
