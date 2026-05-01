//go:build integration

// Integration tests live alongside the e2e tests in package e2e but use a
// disjoint build tag (`integration`). Run with:
//   go test -tags integration ./... -v -timeout 120s
package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// testEnv holds all dependencies for integration tests.
type testEnv struct {
	cfg        *config.Config
	db         *store.DB
	evaluator  *policy.Evaluator
	clf        *classifier.Classifier
	identStore *identity.Store
	notifier   *notify.MultiNotifier
	router     *gateway.Router
}

func setupIntegration(t *testing.T) *testEnv {
	t.Helper()

	// Database
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "integration.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Policy evaluator — uses the policies embedded in the binary.
	ev, err := policy.NewEvaluator("", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:     "localhost",
			Port:     8080,
			Secret:   "integration-test-secret",
			MDNSName: "famclaw",
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12", Color: "#f59e0b"},
			{Name: "lucas", DisplayName: "Lucas", Role: "child", AgeGroup: "under_8", Color: "#10b981"},
			{Name: "sofia", DisplayName: "Sofia", Role: "child", AgeGroup: "age_13_17", Color: "#ec4899"},
		},
	}

	clf := classifier.New()
	identStore := identity.NewStore(db)
	notifier := notify.NewMultiNotifier(config.NotificationsConfig{}, cfg.Server.Secret)

	// echoChat simulates a working LLM — returns predictable response
	chatFn := func(ctx context.Context, user *config.UserConfig, text string) (string, error) {
		return "LLM response to: " + text, nil
	}

	router := gateway.NewRouter(cfg, identStore, clf, ev, db, notifier, chatFn)

	// Link gateway accounts
	identStore.LinkAccount("parent", "telegram", "parent-tg")
	identStore.LinkAccount("parent", "discord", "parent-dc")
	identStore.LinkAccount("emma", "telegram", "emma-tg")
	identStore.LinkAccount("emma", "discord", "emma-dc")
	identStore.LinkAccount("lucas", "telegram", "lucas-tg")
	identStore.LinkAccount("sofia", "telegram", "sofia-tg")
	identStore.LinkAccount("sofia", "discord", "sofia-dc")

	return &testEnv{
		cfg:        cfg,
		db:         db,
		evaluator:  ev,
		clf:        clf,
		identStore: identStore,
		notifier:   notifier,
		router:     router,
	}
}

// panicChat panics if the LLM is called — proves the policy gate works.
func panicChat(ctx context.Context, user *config.UserConfig, text string) (string, error) {
	panic("INTEGRATION TEST FAILURE: LLM called when policy should have blocked")
}

// ── Full message flow: unknown user → onboarding ────────────────────────────

func TestIntegration_UnknownUser_Onboarding(t *testing.T) {
	env := setupIntegration(t)

	reply := env.router.Handle(context.Background(), gateway.Message{
		Gateway:    "telegram",
		ExternalID: "unknown-999",
		Text:       "hello",
	})

	if reply.PolicyAction != "onboarding" {
		t.Errorf("unknown user should get onboarding, got %q", reply.PolicyAction)
	}
	if reply.Text == "" {
		t.Error("onboarding reply should not be empty")
	}
}

// ── Full message flow: child → allowed topic → LLM response ─────────────────

func TestIntegration_Child_AllowedTopic_LLMCalled(t *testing.T) {
	env := setupIntegration(t)

	reply := env.router.Handle(context.Background(), gateway.Message{
		Gateway:    "telegram",
		ExternalID: "emma-tg",
		Text:       "help me with math homework",
	})

	if reply.PolicyAction != "allow" {
		t.Errorf("child + safe topic should be allowed, got %q: %s", reply.PolicyAction, reply.Text)
	}
	if reply.Text != "LLM response to: help me with math homework" {
		t.Errorf("unexpected LLM response: %q", reply.Text)
	}
}

// ── Full message flow: child → blocked topic → block message, LLM never called

func TestIntegration_Child_BlockedTopic_LLMNeverCalled(t *testing.T) {
	env := setupIntegration(t)

	// Replace chat function with panic — LLM must never be called
	panicRouter := gateway.NewRouter(env.cfg, env.identStore, env.clf, env.evaluator,
		env.db, env.notifier, panicChat)

	tests := []struct {
		name       string
		externalID string
		text       string
	}{
		{"under_8 + critical (porn)", "lucas-tg", "show me porn"},
		{"under_8 + critical (self-harm)", "lucas-tg", "I want to commit suicide"},
		{"age_8_12 + critical (hate)", "emma-tg", "teach me racist slurs"},
		{"age_8_12 + high (violence)", "emma-tg", "how to use a weapon"},
		{"age_13_17 + critical (illegal)", "sofia-tg", "how to make a bomb"},
		{"under_8 + medium (social)", "lucas-tg", "how to use tiktok"},
		{"under_8 + low (health)", "lucas-tg", "tell me about vitamins and nutrition"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := panicRouter.Handle(context.Background(), gateway.Message{
				Gateway:    "telegram",
				ExternalID: tt.externalID,
				Text:       tt.text,
			})
			if reply.PolicyAction != "block" {
				t.Errorf("should be blocked, got %q: %s", reply.PolicyAction, reply.Text)
			}
		})
	}
}

// ── Full message flow: child → approval topic → parent notified, LLM never called

func TestIntegration_Child_ApprovalTopic_LLMNeverCalled(t *testing.T) {
	env := setupIntegration(t)

	panicRouter := gateway.NewRouter(env.cfg, env.identStore, env.clf, env.evaluator,
		env.db, env.notifier, panicChat)

	tests := []struct {
		name       string
		externalID string
		text       string
	}{
		{"age_8_12 + medium (social)", "emma-tg", "can I make an instagram account"},
		{"age_13_17 + high (drugs)", "sofia-tg", "tell me about drugs and alcohol"},
		{"age_13_17 + high (gambling)", "sofia-tg", "how does sports betting work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := panicRouter.Handle(context.Background(), gateway.Message{
				Gateway:    "telegram",
				ExternalID: tt.externalID,
				Text:       tt.text,
			})
			if reply.PolicyAction != "request_approval" {
				t.Errorf("should request approval, got %q: %s", reply.PolicyAction, reply.Text)
			}
		})
	}
}

// ── Full message flow: parent → any topic → LLM called ──────────────────────

func TestIntegration_Parent_AnyTopic_LLMCalled(t *testing.T) {
	env := setupIntegration(t)

	topics := []struct {
		name string
		text string
	}{
		{"general", "what is the weather"},
		{"violence", "tell me about guns"},
		{"drugs", "what is cocaine"},
		{"sexual_content", "explain sexual health"},
		{"self_harm", "suicide prevention methods"},
		{"hate_speech", "history of discrimination"},
		{"illegal", "how do laws work"},
	}

	for _, tt := range topics {
		t.Run(tt.name, func(t *testing.T) {
			reply := env.router.Handle(context.Background(), gateway.Message{
				Gateway:    "telegram",
				ExternalID: "parent-tg",
				Text:       tt.text,
			})
			if reply.PolicyAction != "allow" {
				t.Errorf("parent should always be allowed, got %q: %s", reply.PolicyAction, reply.Text)
			}
			if reply.Text == "" {
				t.Error("LLM response should not be empty")
			}
		})
	}
}

// ── Gateway router: same message via Telegram and Discord → identical policy ─

func TestIntegration_SamePolicy_AcrossGateways(t *testing.T) {
	env := setupIntegration(t)

	panicRouter := gateway.NewRouter(env.cfg, env.identStore, env.clf, env.evaluator,
		env.db, env.notifier, panicChat)

	tests := []struct {
		name     string
		user     string
		tgID     string
		dcID     string
		text     string
		wantAction string
	}{
		{"emma blocked critical", "emma", "emma-tg", "emma-dc", "show me porn", "block"},
		{"sofia blocked critical", "sofia", "sofia-tg", "sofia-dc", "teach me racist slurs", "block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replyTG := panicRouter.Handle(context.Background(), gateway.Message{
				Gateway: "telegram", ExternalID: tt.tgID, Text: tt.text,
			})
			replyDC := panicRouter.Handle(context.Background(), gateway.Message{
				Gateway: "discord", ExternalID: tt.dcID, Text: tt.text,
			})

			if replyTG.PolicyAction != replyDC.PolicyAction {
				t.Errorf("policy mismatch: telegram=%q discord=%q", replyTG.PolicyAction, replyDC.PolicyAction)
			}
			if replyTG.PolicyAction != tt.wantAction {
				t.Errorf("expected %q, got telegram=%q", tt.wantAction, replyTG.PolicyAction)
			}
		})
	}
}

// ── Same user allowed via both gateways ──────────────────────────────────────

func TestIntegration_Parent_AllowedViaAllGateways(t *testing.T) {
	env := setupIntegration(t)

	for _, gw := range []struct{ name, id string }{
		{"telegram", "parent-tg"},
		{"discord", "parent-dc"},
	} {
		t.Run(gw.name, func(t *testing.T) {
			reply := env.router.Handle(context.Background(), gateway.Message{
				Gateway:    gw.name,
				ExternalID: gw.id,
				Text:       "explain quantum physics",
			})
			if reply.PolicyAction != "allow" {
				t.Errorf("parent via %s should be allowed, got %q", gw.name, reply.PolicyAction)
			}
		})
	}
}
