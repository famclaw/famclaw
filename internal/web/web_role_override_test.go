package web

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// TestServerResolveUserRoleFromDB verifies that resolveUserRole returns the
// overridden role/age when a DB-persisted override exists, and the config
// role/age when no override exists.  This exercises the exact code path
// handleChat uses to build adjustedUser before creating the agent.
func TestServerResolveUserRoleFromDB(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
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
		},
	}

	identStore := identity.NewStore(db)
	clf := classifier.New()

	s := &Server{
		cfg:        cfg,
		db:         db,
		identStore: identStore,
		evaluator:  ev,
		clf:        clf,
		cfgMu:      sync.RWMutex{},
	}
	ctx := context.Background()

	// --- No override: resolveUserRole returns config values ---
	user := s.resolveUserRole(ctx, "emma")
	if user == nil {
		t.Fatal("resolveUserRole returned nil for emma")
	}
	if user.Role != "child" {
		t.Errorf("no override: Role = %q, want %q", user.Role, "child")
	}
	if user.AgeGroup != "age_8_12" {
		t.Errorf("no override: AgeGroup = %q, want %q", user.AgeGroup, "age_8_12")
	}

	// Verify emma (age_8_12) would request_approval for social media.
	decision, err := ev.Evaluate(ctx, policy.Input{
		User:  policy.UserInput{Role: user.Role, AgeGroup: user.AgeGroup, Name: "emma"},
		Query: policy.QueryInput{Category: "social_media", Text: "can I use instagram and tiktok"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != "request_approval" {
		t.Errorf("no override: PolicyAction = %q, want request_approval", decision.Action)
	}

	// --- Set override: emma → under_8 ---
	err = db.SetRoleOverride(ctx, "emma", "child", "under_8", "parent")
	if err != nil {
		t.Fatalf("SetRoleOverride: %v", err)
	}
	defer db.SetRoleOverride(ctx, "emma", "", "", "parent")

	// Verify resolveUserRole picks up the override.
	user = s.resolveUserRole(ctx, "emma")
	if user == nil {
		t.Fatal("resolveUserRole returned nil for emma after override")
	}
	if user.Role != "child" {
		t.Errorf("with override: Role = %q, want %q", user.Role, "child")
	}
	if user.AgeGroup != "under_8" {
		t.Errorf("with override: AgeGroup = %q, want %q", user.AgeGroup, "under_8")
	}

	// The resolved user must differ from config (proves the copy-and-override path was taken).
	configUser := cfg.GetUser("emma")
	if user.Role == configUser.Role && user.AgeGroup == configUser.AgeGroup {
		t.Error("resolveUserRole returned config values despite an override being set")
	}

	// Verify emma (under_8) is blocked from social media.
	decision, err = ev.Evaluate(ctx, policy.Input{
		User:  policy.UserInput{Role: user.Role, AgeGroup: user.AgeGroup, Name: "emma"},
		Query: policy.QueryInput{Category: "social_media", Text: "can I use instagram and tiktok"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision.Action != "block" {
		t.Errorf("with under_8 override: PolicyAction = %q, want block", decision.Action)
	}
	if decision.Reason == "" {
		t.Error("expected a block reason in the decision, got empty reason")
	}
}
