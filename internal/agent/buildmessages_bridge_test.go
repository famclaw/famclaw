package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// TestBuildMessagesReusesSharedOpaqueBridge verifies the factual boundary
// that guides finding 53fddc3f3e30 targets: Agent.buildMessages must not
// maintain its own lossy compressor. It reuses the SAME opaque LLM→compress
// bridge + CompressIndices + position projection that the tool loop and
// StageCompress use (agentcore.LLMToCompress / agentcore.CompressToLLM).
//
// Evidence: under a tight context budget the shared bridge performs
// group-aware eviction and projects the original llm.Message objects back
// BY POSITION — evicted prunable messages disappear, and the protected
// system prompt and current user message are returned verbatim. This is
// the direct-build path using the one shared compression bridge, not a
// second hand-rolled copy.
//
// Defensive/lossless boundary: normal DB history is plain role+content
// (no tool-call metadata), so buildMessages constructs history from
// store.Role/Content plus the current user message. The bridge becomes
// complete when a source actually carries tool-call data; it never invents
// IDs for plain history.
func TestBuildMessagesReusesSharedOpaqueBridge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const convID = "bridge-e2e"

	// Plain DB history: role + content only, exactly what
	// GetConversationHistory returns. A large OLD user message, then several
	// small fillers, then the current user message. The OLD message is the
	// oldest prunable message and must fall outside the "last 3 protected"
	// window so the shared bridge evicts it under the tight budget.
	if err := db.SaveMessage(convID, "parent", "user", strings.Repeat("OLD", 3000), "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", "acknowledged the older request", "", "allow", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "user", "filler question one", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", "filler answer one", "", "allow", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "user", "filler question two", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", "filler answer two", "", "allow", ""); err != nil {
		t.Fatal(err)
	}
	currentUser := "current question"
	if err := db.SaveMessage(convID, "parent", "user", currentUser, "", "", ""); err != nil {
		t.Fatal(err)
	}

	cfg := staleSearchTestCfg("")
	// Tight budget so the shared bridge must evict the large prunable pairs.
	cfg.LLM.MaxContextTokens = 400
	cfg.LLM.MaxResponseTokens = 40

	ev, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("policy.NewEvaluator: %v", err)
	}
	a, err := NewAgent(&cfg.Users[0], cfg, nil, ev, classifier.New(), db, AgentDeps{
		MsgContext: gateway.MsgContext{ConvID: convID},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	history, _ := db.GetConversationHistory(convID, 20)
	msgs := a.buildMessages(context.Background(), history, currentUser)

	// System prompt always retained (index 0).
	if msgs[0].Role != "system" {
		t.Fatalf("expected system prompt first, got %q", msgs[0].Role)
	}

	// The large OLD prunable user message must be evicted under the tight
	// budget — proving the shared bridge actually compresses, not passes
	// everything through.
	sawOld := false
	for _, m := range msgs {
		if m.Content == strings.Repeat("OLD", 3000) {
			sawOld = true
		}
	}
	if sawOld {
		t.Error("large prunable OLD message survived tight budget — shared bridge should evict it")
	}

	// The current user message must survive, returned verbatim by position
	// (the shared bridge projects the original llm.Message object back).
	sawCurrent := false
	for _, m := range msgs {
		if m.Role == "user" && m.Content == currentUser {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Error("current user message was evicted; CompressIndices protects the most recent user-role message")
	}

	// Position projection preserves role/content of retained messages
	// exactly — no reconstructed or blanked content. Every retained
	// non-system message must be a known original.
	known := map[string]bool{
		currentUser:                      true,
		"acknowledged the older request": true,
		"filler question one":            true,
		"filler answer one":              true,
		"filler question two":            true,
		"filler answer two":              true,
	}
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		if !known[m.Content] {
			t.Errorf("retained %s message with unrecognized/blanked content %q — position projection must return original objects", m.Role, m.Content)
		}
	}
}
