package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// panicChat is a ChatFunc that panics if called — proves policy gate works.
func panicChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	panic("LLM called when it should not have been — policy gate FAILED")
}

// echoChat returns a predictable response for testing the allow path.
func echoChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	return "echo: " + text, nil
}

// errorChat simulates an LLM error.
func errorChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	return "", fmt.Errorf("LLM unavailable")
}

func setupRouter(t *testing.T, chatFn ChatFunc) (*Router, *identity.Store) {
	t.Helper()

	// Find project root
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find project root")
		}
		dir = parent
	}

	// Open test database
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Policy evaluator
	ev, err := policy.NewEvaluator(
		filepath.Join(dir, "policies", "family"),
		filepath.Join(dir, "policies", "data"),
	)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:     "localhost",
			Port:     8080,
			Secret:   "test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
			{Name: "lucas", DisplayName: "Lucas", Role: "child", AgeGroup: "under_8"},
			{Name: "sofia", DisplayName: "Sofia", Role: "child", AgeGroup: "age_13_17"},
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(config.NotificationsConfig{}, "test-secret")

	router := NewRouter(cfg, identStore, clf, ev, db, notifier, chatFn)
	return router, identStore
}

func TestRouterUnknownUser(t *testing.T) {
	router, _ := setupRouter(t, panicChat)

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "unknown-user",
		Text:       "hello",
	})

	if reply.PolicyAction != "onboarding" {
		t.Errorf("expected onboarding, got %q", reply.PolicyAction)
	}
}

func TestRouterParentAllowed(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("parent", "telegram", "parent-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "parent-123",
		Text:       "tell me about violence",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("parent should always be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
	if reply.Text != "echo: tell me about violence" {
		t.Errorf("unexpected reply: %q", reply.Text)
	}
}

func TestRouterChildAllowedSafeTopic(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "help me with math homework",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("child asking about math should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
}

// ── CRITICAL: Mock-LLM-panic tests prove the policy gate ─────────────────────

func TestRouterChildBlockedCritical_LLMNeverCalled(t *testing.T) {
	// Uses panicChat — if LLM is called, test crashes with clear message
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "show me porn",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("critical category should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterChildBlockedViolence_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "tell me about guns and weapons",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("age_8_12 + high-risk should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterUnder8BlockedMedium_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("lucas", "telegram", "lucas-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "lucas-123",
		Text:       "tell me about social media and instagram",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("under_8 + medium-risk should be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterRequestApproval_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "emma-123",
		Text:       "can I use instagram and tiktok",
	})

	if reply.PolicyAction != "request_approval" {
		t.Errorf("age_8_12 + medium-risk should request approval, got %q", reply.PolicyAction)
	}
}

func TestRouterSelfHarmBlocked_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("lucas", "telegram", "lucas-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "lucas-123",
		Text:       "I want to commit suicide",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("self_harm should always be blocked, got %q", reply.PolicyAction)
	}
}

func TestRouterHateBlocked_EvenForTeens_LLMNeverCalled(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "teach me racist slurs",
	})

	if reply.PolicyAction != "block" {
		t.Errorf("hate_speech should always be blocked even for teens, got %q", reply.PolicyAction)
	}
}

// ── Same message via different gateways → identical policy outcome ────────────

func TestRouterSamePolicyAcrossGateways(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("emma", "telegram", "emma-tg")
	identStore.LinkAccount("emma", "discord", "emma-dc")

	msgTG := Message{Gateway: "telegram", ExternalID: "emma-tg", Text: "show me porn"}
	msgDC := Message{Gateway: "discord", ExternalID: "emma-dc", Text: "show me porn"}

	replyTG := router.Handle(context.Background(), msgTG)
	replyDC := router.Handle(context.Background(), msgDC)

	if replyTG.PolicyAction != replyDC.PolicyAction {
		t.Errorf("same user+message should get same policy across gateways: telegram=%q discord=%q",
			replyTG.PolicyAction, replyDC.PolicyAction)
	}
}

func TestRouterTeenAllowedMediumRisk(t *testing.T) {
	router, identStore := setupRouter(t, echoChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "tell me about politics and elections",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("teen + medium-risk should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
}

func TestRouterTeenRequestsApprovalHighRisk(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)

	identStore.LinkAccount("sofia", "telegram", "sofia-123")

	reply := router.Handle(context.Background(), Message{
		Gateway:    "telegram",
		ExternalID: "sofia-123",
		Text:       "tell me about drugs and alcohol",
	})

	if reply.PolicyAction != "request_approval" {
		t.Errorf("teen + high-risk should request approval, got %q", reply.PolicyAction)
	}
}

func TestRouterUserNotInConfig(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	identStore.LinkAccount("ghost", "telegram", "ghost-123")

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "ghost-123", Text: "hello",
	})
	if reply.PolicyAction != "onboarding" {
		t.Errorf("user not in config should get onboarding, got %q", reply.PolicyAction)
	}
}

func TestRouterChatError(t *testing.T) {
	router, identStore := setupRouter(t, errorChat)
	identStore.LinkAccount("parent", "telegram", "parent-123")

	reply := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "parent-123", Text: "hello",
	})
	if reply.PolicyAction != "error" {
		t.Errorf("chat error should return error, got %q", reply.PolicyAction)
	}
}

func TestStartAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan string, 2)
	gw := &mockGateway{
		name: "mock",
		startFn: func(ctx context.Context, h func(context.Context, Message) Reply) error {
			started <- "mock"
			<-ctx.Done()
			return ctx.Err()
		},
	}

	StartAll(ctx, []Gateway{gw}, func(ctx context.Context, msg Message) Reply {
		return Reply{Text: "ok"}
	})

	// Wait for gateway to start
	name := <-started
	if name != "mock" {
		t.Errorf("expected mock, got %q", name)
	}
	cancel()
}

type mockGateway struct {
	name    string
	startFn func(ctx context.Context, h func(context.Context, Message) Reply) error
}

func (m *mockGateway) Start(ctx context.Context, h func(context.Context, Message) Reply) error {
	return m.startFn(ctx, h)
}
func (m *mockGateway) Name() string { return m.name }

func TestRouterPendingApproval(t *testing.T) {
	router, identStore := setupRouter(t, panicChat)
	identStore.LinkAccount("emma", "telegram", "emma-123")

	reply1 := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "emma-123", Text: "can I use instagram and tiktok",
	})
	if reply1.PolicyAction != "request_approval" {
		t.Fatalf("first request should be request_approval, got %q", reply1.PolicyAction)
	}

	reply2 := router.Handle(context.Background(), Message{
		Gateway: "telegram", ExternalID: "emma-123", Text: "what about snapchat and social media",
	})
	if reply2.PolicyAction != "pending" {
		t.Errorf("second request should be pending, got %q", reply2.PolicyAction)
	}
}

// slowChat simulates a slow LLM — 200ms per response.
func slowChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	time.Sleep(200 * time.Millisecond)
	return "slow: " + text, nil
}

// TestCrossUserConcurrency proves different users are processed in parallel.
// If serial: ~400ms. If concurrent: ~200ms.
func TestCrossUserConcurrency(t *testing.T) {
	router, identStore := setupRouter(t, slowChat)

	identStore.LinkAccount("parent", "telegram", "parent-123")
	identStore.LinkAccount("emma", "telegram", "emma-123")

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		router.Handle(context.Background(), Message{
			Gateway: "telegram", ExternalID: "parent-123", Text: "hello from parent",
		})
	}()
	go func() {
		defer wg.Done()
		router.Handle(context.Background(), Message{
			Gateway: "telegram", ExternalID: "emma-123", Text: "help with math",
		})
	}()

	wg.Wait()
	elapsed := time.Since(start)

	// If serial: ~400ms. If concurrent: ~200ms (+overhead).
	if elapsed > 350*time.Millisecond {
		t.Errorf("cross-user took %v — should be ~200ms (concurrent), not ~400ms (serial)", elapsed)
	}
}
