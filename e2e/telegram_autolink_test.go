//go:build integration

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

func TestTelegram_UnknownAccount_AutoLink(t *testing.T) {
	tmp := t.TempDir()
	db, err := store.Open(filepath.Join(tmp, "autolink.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "localhost", Port: 8080, Secret: "test-secret", MDNSName: "famclaw"},
		LLM:    config.LLMConfig{Temperature: 0.7, MaxResponseTokens: 256},
		Users: []config.UserConfig{
			{Name: "alpha", DisplayName: "Alpha", Role: "child", AgeGroup: "age_8_12"},
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()
	notifier := notify.NewMultiNotifier(config.NotificationsConfig{}, cfg.Server.Secret)
	ev, err := policy.NewEvaluator("", "")
	if err != nil {
		t.Fatalf("policy.NewEvaluator: %v", err)
	}
	chatFn := func(ctx context.Context, u *config.UserConfig, text string) (string, error) {
		return "stub", nil
	}
	router := gateway.NewRouter(cfg, identStore, clf, ev, db, notifier, chatFn)

	ctx := context.Background()
	reply := router.Handle(ctx, gateway.Message{
		Gateway:     "telegram",
		ExternalID:  "tg-test-9999",
		DisplayName: "Alpha",
		Text:        "hi",
	})

	if reply.PolicyAction != "onboarding" {
		t.Errorf("expected PolicyAction 'onboarding', got %q", reply.PolicyAction)
	}
	if !strings.Contains(strings.ToLower(reply.Text), "linked") {
		t.Errorf("expected 'linked' in reply text, got %q", reply.Text)
	}

	resolved, err := identStore.Resolve("telegram", "tg-test-9999")
	if err != nil {
		t.Fatalf("identStore.Resolve: %v", err)
	}
	if resolved == nil {
		t.Fatalf("Resolve returned nil after auto-link")
	}
	if resolved.Name != "alpha" {
		t.Errorf("expected resolved.Name 'alpha', got %q", resolved.Name)
	}

	unknowns, err := identStore.ListUnknown(ctx)
	if err != nil {
		t.Fatalf("identStore.ListUnknown: %v", err)
	}
	if len(unknowns) != 0 {
		t.Errorf("expected 0 unknown after auto-link, got %d", len(unknowns))
	}
}
