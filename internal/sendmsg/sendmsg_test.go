package sendmsg

import (
	"context"
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
	failOn int  // 0 = never fail; n>0 fails on the nth call
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
		extraGW    string // gateway to register a sender for beyond telegram/discord
		wantErr    bool
		wantErrSub string
	}{
		{
			name:      "deliver to telegram user",
			to:        "julia",
			message:   "hi from mom",
			setupDB:   func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:   false,
		},
		{
			name:      "deliver to discord user",
			to:        "julia",
			message:   "hello via discord",
			setupDB:   func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "discord", "julia-disc") },
			wantErr:   false,
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
			wantErrSub: "has not sent any messages yet",
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
			name:      "empty message",
			to:        "julia",
			message:   "",
			setupDB:   func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:   true,
			wantErrSub: "requires both",
		},
		{
			name:       "gateway has no sender",
			to:         "julia",
			message:    "hi",
			setupDB:    func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "whatsapp", "julia-wa") },
			wantErr:    true,
			wantErrSub: "no sender available for gateway",
		},
		{
			name:      "sender fails",
			to:        "julia",
			message:   "hi",
			setupDB:   func(t *testing.T, db *store.DB) { linkUser(t, db, "julia", "telegram", "julia-chat") },
			wantErr:   true,
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

			_, err := Handle(ctx, db, parentCfg(), senders, tc.to, tc.message)

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

	result, err := Handle(ctx, db, parentCfg(), senders, "julia", "message to julia")
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

	_, err := Handle(ctx, db, parentCfg(), senders, "julia", "single message")
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
