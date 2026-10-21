package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestHandleSearchConversations_ScopeFromAgentUser proves the dispatch
// layer scopes search to the agent's own user (a.user.Name) and that
// LLM-supplied args cannot override that scope.
func TestHandleSearchConversations_ScopeFromAgentUser(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	if err := db.SaveMessage("conv-alice", "alice", "user", "planning lunch with the team", "", "", "web"); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := db.SaveMessage("conv-bob", "bob", "user", "bob's secret sushi spot for lunch", "", "", "web"); err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	cfg := &config.Config{Users: []config.UserConfig{
		{Name: "alice", Role: "parent"},
	}}
	a := &Agent{db: db, cfg: cfg, user: &cfg.Users[0]}
	h := a.makeBuiltinHandler()

	// Normal call: returns alice's own message only.
	out, err := h(ctx, "builtin__search_conversations", map[string]any{"query": "lunch"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, "conv-alice") {
		t.Fatalf("expected alice's conversation in output:\n%s", out)
	}
	if strings.Contains(out, "sushi") {
		t.Fatalf("bob's message leaked into alice's results:\n%s", out)
	}

	// Args cannot override scope: passing another user's name (and even a
	// query term that only exists in bob's corpus) must not leak bob's row.
	out, err = h(ctx, "builtin__search_conversations", map[string]any{
		"query": "sushi",
		"user":  "bob", // must be ignored by the dispatch layer
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if strings.Contains(out, "sushi") {
		t.Fatalf("args.user override leaked another user's message:\n%s", out)
	}
	if !strings.Contains(out, "No matches.") {
		t.Fatalf("expected 'No matches.' for out-of-scope query, got:\n%s", out)
	}

	// Date range + max_results args flow through from the LLM args.
	out, err = h(ctx, "builtin__search_conversations", map[string]any{
		"query":       "lunch",
		"from_date":   "2020-01-01",
		"to_date":     "2999-12-31",
		"max_results": float64(5),
	})
	if err != nil {
		t.Fatalf("handler with range args: %v", err)
	}
	if !strings.Contains(out, "conv-alice") {
		t.Fatalf("expected alice's message with range args:\n%s", out)
	}
}
