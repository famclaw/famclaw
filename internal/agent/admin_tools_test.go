package agent

import (
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// adminToolNames is the set of admin builtin tool names (read-only +
// mutating). It is computed from the authoritative AllDefinitions() so the
// assertion tracks the real surface, not a hand-maintained list.
func adminToolNames() map[string]bool {
	names := make(map[string]bool)
	for _, def := range admin.AllDefinitions() {
		names[def.Name] = true
	}
	return names
}

// newRoleAgent builds a minimal agent for the given user (parent or child)
// so the test can inspect which builtin tools were injected. It mirrors
// setupAgent's wiring but lets the caller pick the role under test. No LLM
// calls are made — NewAgent only records the tool slice.
func newRoleAgent(t *testing.T, user *config.UserConfig, cfg *config.Config, serverURL string) *Agent {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}

	clf := classifier.New()
	client := llm.NewClient(serverURL, "test", "")
	a, err := NewAgent(user, cfg, client, ev, clf, db, AgentDeps{})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	return a
}

// TestAgentAdminToolsRoleGated (design note D4, T5-adjacent): admin tool
// definitions are injected into the agent's builtinTools only when the
// RESOLVED user's base role is "parent". This pins the claim that a parent
// previewing a child via ?user= runs the agent built from the PREVIEWED
// user — so the previewed child's agent has no admin tools, and admin power
// cannot ride the parent's session into a child chat.
func TestAgentAdminToolsRoleGated(t *testing.T) {
	wantAdmin := adminToolNames()
	if len(wantAdmin) == 0 {
		t.Fatalf("admin.AllDefinitions() returned no tools; assertion is vacuous")
	}

	// One LLM mock server shared by both agents; no LLM calls are made —
	// NewAgent only records the tool slice.
	server := mockLLMServer(t, nil)
	defer server.Close()

	tests := []struct {
		name      string
		role      string
		wantAdmin bool
	}{
		{name: "parent agent gets admin tools", role: "parent", wantAdmin: true},
		{name: "child agent gets no admin tools", role: "child", wantAdmin: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				LLM: config.LLMConfig{
					BaseURL:           server.URL,
					Model:             "test",
					Temperature:       0.7,
					MaxResponseTokens: 100,
				},
				Users: []config.UserConfig{
					{Name: "parent", DisplayName: "Parent", Role: "parent"},
					{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
				},
			}
			var user *config.UserConfig
			if tt.role == "parent" {
				user = &cfg.Users[0]
			} else {
				user = &cfg.Users[1]
			}

			a := newRoleAgent(t, user, cfg, server.URL)

			got := make(map[string]bool)
			for _, tool := range a.builtinTools {
				got[tool.Name] = true
			}

			if tt.wantAdmin {
				for name := range wantAdmin {
					if !got[name] {
						t.Errorf("parent agent missing admin tool %q (have %d builtins)", name, len(got))
					}
				}
			} else {
				for name := range got {
					if wantAdmin[name] {
						t.Errorf("child agent unexpectedly has admin tool %q (role=%q)", name, tt.role)
					}
				}
			}
		})
	}
}
