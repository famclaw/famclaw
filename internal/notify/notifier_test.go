package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/store"
)

var testApproval = &store.Approval{
	ID:          "test-123",
	UserName:    "alice",
	UserDisplay: "Alice",
	AgeGroup:    "age_8_12",
	Category:    "social_media",
	QueryText:   "Can I make an Instagram account?",
	Status:      "pending",
}

func TestGenerateToken(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret-key-32chars-minimum-xxxxx")
	if token == "" {
		t.Error("token should not be empty")
	}
	if len(token) < 40 {
		t.Errorf("token too short: %d", len(token))
	}
}

func TestGenerateTokenDifferentActions(t *testing.T) {
	approve := GenerateToken("req-1", "approve", "secret")
	deny := GenerateToken("req-1", "deny", "secret")
	if approve == deny {
		t.Error("different actions should produce different tokens")
	}
}

func TestVerifyTokenValid(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")
	id, action, err := VerifyToken(token, "secret", 24)
	if err != nil {
		t.Fatalf("VerifyToken error: %v", err)
	}
	if id != "req-1" {
		t.Errorf("id = %q, want req-1", id)
	}
	if action != "approve" {
		t.Errorf("action = %q, want approve", action)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")
	_, _, err := VerifyToken(token, "wrong-secret", 24)
	if err == nil {
		t.Error("expected error for wrong secret")
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	token := GenerateToken("req-1", "approve", "secret")
	_, _, err := VerifyToken(token, "secret", -1)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestVerifyTokenBadEncoding(t *testing.T) {
	_, _, err := VerifyToken("not-valid-base64!!!", "secret", 24)
	if err == nil {
		t.Error("expected error for bad encoding")
	}
}

func TestVerifyTokenReturnsIDAndAction(t *testing.T) {
	token := GenerateToken("approval-xyz", "deny", "my-secret")
	id, action, err := VerifyToken(token, "my-secret", 24)
	if err != nil {
		t.Fatal(err)
	}
	if id != "approval-xyz" || action != "deny" {
		t.Errorf("got id=%q action=%q", id, action)
	}
}

// TestMultiNotifierSendsToParentGateway verifies that Notify delivers the
// approval message through the parent's linked gateway via sendFn.
func TestMultiNotifierSendsToParentGateway(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}

	if err := identStore.LinkAccount("parent", "telegram", "parent-tg"); err != nil {
		t.Fatalf("link account: %v", err)
	}

	var sentText, sentChatID, sentGateway string
	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		sentGateway = gw
		sentChatID = chatID
		sentText = text
		return nil
	})

	approveURL := "http://example.com/decide?id=1&action=approve&token=abc"
	denyURL := "http://example.com/decide?id=1&action=deny&token=def"
	notifier.Notify(context.Background(), testApproval, approveURL, denyURL)

	if sentGateway != "telegram" {
		t.Errorf("gateway = %q, want telegram", sentGateway)
	}
	if sentChatID != "parent-tg" {
		t.Errorf("chatID = %q, want parent-tg", sentChatID)
	}
	if sentText == "" {
		t.Error("notification text should not be empty")
	}
	if !strings.Contains(sentText, "Approval Request") {
		t.Errorf("notification text should contain approval header, got: %s", sentText)
	}
	if !strings.Contains(sentText, approveURL) {
		t.Error("notification should contain the approve URL")
	}
	if !strings.Contains(sentText, denyURL) {
		t.Error("notification should contain the deny URL")
	}
}

// TestMultiNotifierSkipsNonParents verifies that child users do not receive
// notifications — only parents are notified.
func TestMultiNotifierSkipsNonParents(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}

	if err := identStore.LinkAccount("parent", "telegram", "parent-tg"); err != nil {
		t.Fatalf("link parent: %v", err)
	}
	if err := identStore.LinkAccount("emma", "telegram", "emma-tg"); err != nil {
		t.Fatalf("link child: %v", err)
	}

	var calls int32
	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	notifier.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("should notify parent only (1 call), got %d", got)
	}
}

// TestMultiNotifierSendsDecision verifies that NotifyDecision delivers the
// decision message to the parent's gateway.
func TestMultiNotifierSendsDecision(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		},
	}

	if err := identStore.LinkAccount("parent", "discord", "parent-dc"); err != nil {
		t.Fatalf("link account: %v", err)
	}

	var sentText string
	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		sentText = text
		return nil
	})

	decision := &store.Approval{
		ID:          "test-123",
		UserDisplay: "Alice",
		Category:    "social_media",
		Status:      "approved",
		DecidedBy:   "Parent",
	}
	notifier.NotifyDecision(context.Background(), decision)

	if !strings.Contains(sentText, "approved") {
		t.Errorf("decision message should contain status, got: %s", sentText)
	}
}

// TestMultiNotifierSendsToManyGateways verifies that a parent linked to
// multiple gateways receives the notification on each.
func TestMultiNotifierSendsToManyGateways(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		},
	}

	if err := identStore.LinkAccount("parent", "telegram", "parent-tg"); err != nil {
		t.Fatalf("link telegram: %v", err)
	}
	if err := identStore.LinkAccount("parent", "discord", "parent-dc"); err != nil {
		t.Fatalf("link discord: %v", err)
	}

	var sentChatIDs []string
	var mu sync.Mutex
	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		mu.Lock()
		sentChatIDs = append(sentChatIDs, chatID)
		mu.Unlock()
		return nil
	})

	notifier.Notify(context.Background(), testApproval, "http://approve", "http://deny")

	if len(sentChatIDs) != 2 {
		t.Errorf("expected 2 deliveries (telegram + discord), got %d: %v", len(sentChatIDs), sentChatIDs)
	}
}

// TestMultiNotifierSendErrorIsHandled verifies that a send error from sendFn
// is logged but does not panic.
func TestMultiNotifierSendErrorIsHandled(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		},
	}

	if err := identStore.LinkAccount("parent", "telegram", "parent-tg"); err != nil {
		t.Fatalf("link account: %v", err)
	}

	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		return fmt.Errorf("gateway down: Post \"https://api.telegram.org/bot123:ABC/sendMessage\": connection refused")
	})

	// Should not panic — the error is logged, not returned.
	notifier.Notify(context.Background(), testApproval, "http://approve", "http://deny")
}

// TestMultiNotifierNoParentAccounts verifies that Notify is a no-op when no
// parent has any linked gateway accounts.
func TestMultiNotifierNoParentAccounts(t *testing.T) {
	db := testDB(t)
	identStore := identity.NewStore(db)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
		},
	}

	notifier := NewMultiNotifier(cfg, identStore, func(ctx context.Context, gw, chatID, text string) error {
		t.Error("sendFn should not be called when parent has no gateway accounts")
		return nil
	})

	notifier.Notify(context.Background(), testApproval, "http://approve", "http://deny")
}

// --- helpers ---

func testDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
