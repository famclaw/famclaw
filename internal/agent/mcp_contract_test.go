package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// MCP tool names are opaque to the core: they carry no provider, domain, or
// calendar semantics. These real Agent.Chat contract tests pin that boundary
// — a renamed external tool must keep working, a provider-neutral
// one-time reconsideration must fire only with zero attempted tools, and a
// completed or failed tool call must never be re-prompted.
//
// TestMCPOpaqueToolContract — a successful call through a renamed opaque MCP
// tool is delivered exactly once; the tool result is not suppressed and no
// recovery re-prompt follows. The calendar-domain request text is fixture
// data only.
func TestMCPOpaqueToolContract(t *testing.T) {
	const want = "Synthetic verified appointment."
	cases := []struct {
		name     string
		toolName string
		userMsg  string
	}{
		{name: "read_records", toolName: "read_records", userMsg: "What is on my calendar?"},
		{name: "resource_snapshot_v2", toolName: "resource_snapshot_v2", userMsg: "What is on my calendar?"},
		{name: "renamed_resource", toolName: "read_records", userMsg: "Check my private resource."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &staleSearchChatter{script: []*llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "opaque", Type: "function", Function: llm.ToolCallFunction{Name: tc.toolName, Arguments: map[string]any{}}}}},
				{Role: "assistant", Content: want},
			}}
			db, err := store.Open(filepath.Join(t.TempDir(), "contract.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			a := buildStaleSearchAgent(t, db, staleSearchTestCfg(""), model, "opaque-contract", false)
			pool := mcp.NewPool("", false, false)
			t.Cleanup(pool.StopAll)
			a.pool = pool
			calls := 0
			server := mcpserver.NewMCPServer("external-resource", "1")
			server.AddTool(mcpgo.NewTool(tc.toolName, mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls++
				return mcpgo.NewToolResultText(`{"records":["Synthetic verified appointment."]}`), nil
			})
			if err := pool.RegisterInProcessServer("opaque-server", server); err != nil {
				t.Fatal(err)
			}
			got, err := a.Chat(context.Background(), tc.userMsg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.Content != want {
				t.Fatalf("opaque MCP result suppressed: calls=%d answer=%q", calls, got.Content)
			}
		})
	}
}

// TestMCPSourceNeutralReconsideration — the one-time reconsideration is
// provider-neutral. When nothing was attempted and the model claims inability,
// the re-prompt steers it to the actually-available tools WITHOUT naming a
// provider. The model's second call picks its own resource — a fake public
// web_search in the public case, a fake opaque MCP read in the private case.
// The test does not assert which resource the model will pick; it asserts the
// core preserved the model-selected resource and did not inject a hardcoded
// public-search directive. A second refusal is delivered as-is.
func TestMCPSourceNeutralReconsideration(t *testing.T) {
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"results":[{"title":"Fresh","url":"https://example.com/fresh","content":"clear skies, 72F"}]}`)
	}))
	defer searxng.Close()

	cases := []struct {
		name          string
		toolMsg       string
		secondAttempt *llm.Message
		wantMCPReads  int
		wantWebHits   int
		wantContent   string
	}{
		{
			name:    "public_search_recoveries",
			toolMsg: "Look up the weather",
			secondAttempt: &llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{{ID: "call_ws", Type: "function", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "anything"}}}},
			},
			wantMCPReads: 0,
			wantWebHits:  1,
			wantContent:  "Clear skies, 72F.",
		},
		{
			name:    "private_request_reconsiders",
			toolMsg: "Check my private resource",
			secondAttempt: &llm.Message{
				Role:      "assistant",
				ToolCalls: []llm.ToolCall{{ID: "call_mr", Type: "function", Function: llm.ToolCallFunction{Name: "read_records", Arguments: map[string]any{}}}},
			},
			wantMCPReads: 1,
			wantWebHits:  0,
			wantContent:  "Verified synthetic private resource.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the shared counter so each case observes its own hits.
			atomic.StoreInt32(&searxngHits, 0)
			mcpReads := 0
			model := &staleSearchChatter{script: []*llm.Message{
				{Role: "assistant", Content: "I cannot access that."},
				tc.secondAttempt,
				{Role: "assistant", Content: tc.wantContent},
			}}
			db, err := store.Open(filepath.Join(t.TempDir(), "retry.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			a := buildStaleSearchAgent(t, db, staleSearchTestCfg(searxng.URL), model, "private-retry", true)
			// Both public web_search and the fake opaque MCP resource are
			// offered this turn; the model's second call is expected to pick
			// one of them.
			pool := mcp.NewPool("", false, false)
			t.Cleanup(pool.StopAll)
			a.pool = pool
			server := mcpserver.NewMCPServer("opaque-resource", "1")
			server.AddTool(mcpgo.NewTool("read_records", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				mcpReads++
				return mcpgo.NewToolResultText(`{"resource":"Verified synthetic private resource."}`), nil
			})
			if err := pool.RegisterInProcessServer("opaque-resource", server); err != nil {
				t.Fatal(err)
			}
			got, err := a.Chat(context.Background(), tc.toolMsg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if model.calls != 3 {
				t.Fatalf("reconsideration calls = %d, want 3 (refusal -> re-prompt -> model-selected tool)", model.calls)
			}
			if got.Content != tc.wantContent {
				t.Fatalf("model-selected resource not preserved: got=%q want=%q", got.Content, tc.wantContent)
			}
			// Assert both counters exactly for both cases:
			//   public:  web=1, mcp=0
			//   private: web=0, mcp=1
			if got := int(atomic.LoadInt32(&searxngHits)); got != tc.wantWebHits {
				t.Errorf("web hit count = %d, want %d", got, tc.wantWebHits)
			}
			if mcpReads != tc.wantMCPReads {
				t.Errorf("MCP read count = %d, want %d", mcpReads, tc.wantMCPReads)
			}
			// The re-prompt directive must not name a specific provider. The
			// base prompt legitimately documents the tools offered this turn;
			// only the directive is under test here.
			if len(model.recorded) < 2 {
				t.Fatalf("recorded LLM inputs = %d, want >= 2", len(model.recorded))
			}
			directive := findDirective(model.recorded[1])
			if directive == "" {
				t.Fatal("re-prompt did not carry the reconsideration directive")
			}
			if strings.Contains(strings.ToLower(directive), "web_search") {
				t.Fatalf("reconsideration hardcodes a public-search provider into an arbitrary request:\n%s", directive)
			}
		})
	}
}

// findDirective returns the reconsideration directive injected after the base
// system prompt, identified by its leading words; the base prompt is a fixed
// fixture that documents the offered tools and is not the directive itself.
func findDirective(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "Reconsider the request using the tools actually offered") {
			return m.Content
		}
	}
	return ""
}

// TestMCPOnlyRetryIntentPrelude — the explicit "try again" prelude path must
// also be provider-neutral: when the user asks to retry and the trailing
// history carries a failure claim, the neutral directive must be in the FIRST
// model input of the turn (prelude), not only in the later backstop. The
// offered tool is an opaque MCP tool, not a public web_search.
func TestMCPOnlyRetryIntentPrelude(t *testing.T) {
	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "read", Type: "function", Function: llm.ToolCallFunction{Name: "read_records", Arguments: map[string]any{}}}}},
		{Role: "assistant", Content: "Verified synthetic record."},
	}}
	db, err := store.Open(filepath.Join(t.TempDir(), "prelude.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := buildStaleSearchAgent(t, db, staleSearchTestCfg(""), model, "prelude-retry", false)
	a.builtinTools = nil
	pool := mcp.NewPool("", false, false)
	t.Cleanup(pool.StopAll)
	a.pool = pool
	calls := 0
	server := mcpserver.NewMCPServer("opaque-resource", "1")
	server.AddTool(mcpgo.NewTool("read_records", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		calls++
		return mcpgo.NewToolResultText(`{"records":["Verified synthetic record."]}`), nil
	})
	if err := pool.RegisterInProcessServer("opaque-resource", server); err != nil {
		t.Fatal(err)
	}
	// Seed the prior failure claim in conversation history so the
	// trailingHistoryHasFailureClaim prelude gate sees it.
	if err := db.SaveMessage(a.convID, "parent", "assistant", "I cannot access that.", "general", "allow", a.gatewayForSave()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(context.Background(), "Try again.", nil); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("expected prelude to steer first-attempt tool call: model.calls=%d", model.calls)
	}
	if len(model.recorded) == 0 {
		t.Fatal("no recorded LLM inputs")
	}
	if d := findDirective(model.recorded[0]); d == "" {
		t.Fatal("neutral directive missing from FIRST model input (prelude path did not fire)")
	}
	if calls != 1 {
		t.Fatalf("MCP-only prelude recovery failed: calls=%d", calls)
	}
}

// TestMCPCompletedActionNotReplayed — after ANY attempted tool (success or
// failure), the final inability answer is delivered without an automatic
// second pipeline run: no tool-result clearing, no re-execution.
func TestMCPCompletedActionNotReplayed(t *testing.T) {
	cases := []struct {
		name    string
		toolErr error
		userMsg string
	}{
		{name: "completed_write", toolErr: nil, userMsg: "Record a synthetic change."},
		{name: "failed_tool", toolErr: errors.New("synthetic backend failure"), userMsg: "Fetch the private record."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &staleSearchChatter{script: []*llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "write", Type: "function", Function: llm.ToolCallFunction{Name: "record_change", Arguments: map[string]any{}}}}},
				{Role: "assistant", Content: "I cannot access the confirmation."},
			}}
			db, err := store.Open(filepath.Join(t.TempDir(), "action.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			a := buildStaleSearchAgent(t, db, staleSearchTestCfg("http://127.0.0.1:1"), model, "write-retry", true)
			pool := mcp.NewPool("", false, false)
			t.Cleanup(pool.StopAll)
			a.pool = pool
			calls := 0
			server := mcpserver.NewMCPServer("opaque-actions", "1")
			server.AddTool(mcpgo.NewTool("record_change", mcpgo.WithReadOnlyHintAnnotation(false)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls++
				if tc.toolErr != nil {
					return nil, tc.toolErr
				}
				return mcpgo.NewToolResultText(`{"status":"done"}`), nil
			})
			if err := pool.RegisterInProcessServer("opaque-actions", server); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Chat(context.Background(), tc.userMsg, nil); err != nil {
				t.Fatal(err)
			}
			if model.calls != 2 || calls != 1 {
				t.Fatalf("attempted tool entered automatic recovery: model=%d tool=%d", model.calls, calls)
			}
		})
	}
}

// TestMCPNoWebBuiltinRetryDisabled — true zero-tools control: when NO tools
// are offered at all (builtins cleared in the fixture so that even the
// parent's default admin tools are not present), the reconsideration must
// not fire even on a refusal-style output. This is the separate control case
// that keeps the predicate honest about "nothing offered" vs "offered but
// not attempted".
func TestMCPNoWebBuiltinRetryDisabled(t *testing.T) {
	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I cannot access that."},
	}}
	db, err := store.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := buildStaleSearchAgent(t, db, staleSearchTestCfg("http://127.0.0.1:1"), model, "no-web-retry", false)
	a.builtinTools = nil
	if _, err := a.Chat(context.Background(), "Check my private resource.", nil); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("reconsideration fired with no tools offered: calls=%d", model.calls)
	}
}

// TestMCPOnlyRetryRecovery — public CI coverage for the MCP-only recovery
// path: when ONLY an opaque MCP tool is offered (builtins cleared so the
// fixture offers nothing but the fake MCP resource) and the model refuses
// without attempting it, the one-time reconsideration must let the model
// pick up its own resource and deliver a real result. Keeping this positive
// case in public CI is what prevents a regression that "fixes" one
// deployment by dropping the MCP-only path.
func TestMCPOnlyRetryRecovery(t *testing.T) {
	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I cannot access that."},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "read", Type: "function", Function: llm.ToolCallFunction{Name: "read_records", Arguments: map[string]any{}}}}},
		{Role: "assistant", Content: "Verified synthetic record."},
	}}
	db, err := store.Open(filepath.Join(t.TempDir(), "mcp-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := buildStaleSearchAgent(t, db, staleSearchTestCfg(""), model, "mcp-only-recovery", false)
	a.builtinTools = nil
	pool := mcp.NewPool("", false, false)
	t.Cleanup(pool.StopAll)
	a.pool = pool
	calls := 0
	server := mcpserver.NewMCPServer("opaque-resource", "1")
	server.AddTool(mcpgo.NewTool("read_records", mcpgo.WithReadOnlyHintAnnotation(true)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		calls++
		return mcpgo.NewToolResultText(`{"records":["Verified synthetic record."]}`), nil
	})
	if err := pool.RegisterInProcessServer("opaque-resource", server); err != nil {
		t.Fatal(err)
	}
	got, err := a.Chat(context.Background(), "Read my private records.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Content != "Verified synthetic record." {
		t.Fatalf("MCP-only recovery failed: calls=%d result=%q", calls, got.Content)
	}
}

// TestMCPBoundedSecondFailure — the reconsideration is one-shot: if the
// re-prompted attempt refuses again without a tool call, that refusal is
// delivered as-is with no further retry loop.
func TestMCPBoundedSecondFailure(t *testing.T) {
	var searxngHits int32
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&searxngHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer searxng.Close()

	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", Content: "I cannot access that."},
		{Role: "assistant", Content: "No verified result is available."},
	}}
	db, err := store.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := buildStaleSearchAgent(t, db, staleSearchTestCfg(searxng.URL), model, "bounded-retry", true)
	resp, err := a.Chat(context.Background(), "Check my private resource.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "No verified result is available." {
		t.Fatalf("Content = %q, want the bounded second refusal delivered as-is", resp.Content)
	}
	if model.calls != 2 {
		t.Fatalf("unbounded recovery: calls=%d", model.calls)
	}
	if got := atomic.LoadInt32(&searxngHits); got != 0 {
		t.Fatalf("search endpoint hit after refusal-only turns: hits=%d", got)
	}
}

// TestAgentChatCompressionAcrossToolRounds is the full Agent.Chat end-to-end
// regression that forces compression across multiple real tool rounds.
// It reuses the real Chat / scripted-model / in-process fake-MCP harness.
//
// Setup:
//   - MaxContextTokens is small (500) so the tool-loop compression fires.
//   - The model issues THREE sequential MCP tool calls (three rounds):
//     round 1: large result (~3000 chars, "call_r1")
//     round 2: small result (~100 chars, "call_r2")
//     round 3: medium result (~500 chars, "call_r3")
//   - After round 3's result is appended, the context is over budget and
//     compression must evict at least one older group.
//   - The tool name is "resource_v2" (an opaque renamed MCP tool).
//
// Assertions:
//   - Compression actually occurred: the final request has fewer messages
//     than the pre-compression total.
//   - No orphaned tool_call_id in any request (bidirectional pairing).
//   - The large group (call_r1) is evicted from the final request.
//   - The small group (call_r2) survives in the final request (positive
//     survival with exact ID and order preserved).
//   - The current round's call (call_r3) is always present (last-3 protection).
//   - Each MCP tool is invoked exactly once per round (no replay): total
//     handler calls = 3.
//   - The final response is the scripted answer.
//   - The tool schema/results are identical regardless of the renamed tool
//     name: the handler receives the same arguments and returns the same
//     content for each round.
//   - Input policy: user messages are preserved (not evicted by compression
//     unless they are the oldest and non-protected).
func TestAgentChatCompressionAcrossToolRounds(t *testing.T) {
	const want = "All three resources collected."
	db, err := store.Open(filepath.Join(t.TempDir(), "compress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const convID = "compress-e2e"

	// Minimal prior history so buildMessages has something to work with.
	if err := db.SaveMessage(convID, "parent", "user", "Collect three resources.", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMessage(convID, "parent", "assistant", "I will collect them now.", "", "allow", ""); err != nil {
		t.Fatal(err)
	}

	cfg := staleSearchTestCfg("")
	cfg.LLM.MaxContextTokens = 500 // tight: forces compression after round 3
	cfg.LLM.MaxResponseTokens = 50

	// Scripted model: three tool-call rounds, then a final answer.
	// Round 1: call_r1 → large result
	// Round 2: call_r2 → small result
	// Round 3: call_r3 → medium result
	// Final: plain text answer
	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_r1", Type: "function", Function: llm.ToolCallFunction{Name: "resource_v2", Arguments: map[string]any{"key": "r1"}}},
		}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_r2", Type: "function", Function: llm.ToolCallFunction{Name: "resource_v2", Arguments: map[string]any{"key": "r2"}}},
		}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_r3", Type: "function", Function: llm.ToolCallFunction{Name: "resource_v2", Arguments: map[string]any{"key": "r3"}}},
		}},
		{Role: "assistant", Content: want},
	}}

	a := buildStaleSearchAgent(t, db, cfg, model, convID, false)
	pool := mcp.NewPool("", false, false)
	t.Cleanup(pool.StopAll)
	a.pool = pool

	// Handler returns differently-sized results per call to simulate
	// real varying tool outputs. The tool name is opaque ("resource_v2").
	var mcpCalls int
	server := mcpserver.NewMCPServer("ext", "1")
	server.AddTool(mcpgo.NewTool("resource_v2", mcpgo.WithReadOnlyHintAnnotation(true)), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		mcpCalls++
		key := ""
		if m, ok := req.Params.Arguments.(map[string]any); ok {
			if s, ok := m["key"].(string); ok {
				key = s
			}
		}
		switch key {
		case "r1":
			return mcpgo.NewToolResultText(strings.Repeat("X", 3000)), nil // large
		case "r2":
			return mcpgo.NewToolResultText("small result data"), nil // ~17 chars
		case "r3":
			return mcpgo.NewToolResultText(strings.Repeat("Y", 500)), nil // medium
		default:
			return mcpgo.NewToolResultText("unknown"), nil
		}
	})
	if err := pool.RegisterInProcessServer("ext-server", server); err != nil {
		t.Fatal(err)
	}

	resp, err := a.Chat(context.Background(), "Collect all three.", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != want {
		t.Fatalf("Content = %q, want %q", resp.Content, want)
	}

	// No replay: each tool call executed exactly once.
	if mcpCalls != 3 {
		t.Fatalf("MCP tool replay: calls=%d, want 3", mcpCalls)
	}
	// Model was called 4 times: 3 tool rounds + 1 final answer.
	if model.calls != 4 {
		t.Fatalf("model calls = %d, want 4", model.calls)
	}

	// The final request (index 3, after round 3's result) must have
	// fewer messages than the un-compressed total.
	// Pre-compression total: system(1) + history(2) + user(1) +
	//   round1: assistant+result(2) + round2: assistant+result(2) +
	//   round3: assistant+result(2) = 12 messages.
	// After compression, some older groups must be evicted.
	finalReq := model.recorded[3]
	if len(finalReq) >= 12 {
		t.Errorf("compression did not occur: final request has %d messages (>= 12)", len(finalReq))
	}

	// Wire invariants on every recorded request.
	for i, msgs := range model.recorded {
		assistantIDs := make(map[string]bool)
		resultIDs := make(map[string]bool)
		for _, m := range msgs {
			if m.Role == "assistant" {
				for _, tc := range m.ToolCalls {
					assistantIDs[tc.ID] = true
				}
			}
			if m.Role == "tool" && m.ToolCallID != "" {
				resultIDs[m.ToolCallID] = true
			}
		}
		// Bidirectional pairing: no orphans in either direction.
		for rid := range resultIDs {
			if !assistantIDs[rid] {
				t.Errorf("request %d: orphaned tool result id=%q (no matching assistant call)", i, rid)
			}
		}
		for aid := range assistantIDs {
			if !resultIDs[aid] {
				t.Errorf("request %d: orphaned assistant call id=%q (no matching tool result)", i, aid)
			}
		}
	}

	// Positive survival: the small group (call_r2) must be present in the
	// final request. The large group (call_r1) must be evicted.
	// The current group (call_r3) must be present (last-3 protection).
	finalIDs := make(map[string]bool)
	for _, m := range finalReq {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				finalIDs[tc.ID] = true
			}
		}
	}
	if !finalIDs["call_r2"] {
		t.Error("positive survival FAILED: small group call_r2 was evicted — no historical group survived")
	}
	if finalIDs["call_r1"] {
		t.Error("eviction FAILED: large group call_r1 is still present — compression did not free space")
	}
	if !finalIDs["call_r3"] {
		t.Error("current-round protection FAILED: call_r3 missing from final request")
	}

	// Verify the surviving group's tool result is intact (exact content).
	for _, m := range finalReq {
		if m.Role == "tool" && m.ToolCallID == "call_r2" {
			if m.Content != "small result data" {
				t.Errorf("call_r2 result corrupted: %q", m.Content)
			}
		}
	}
}

// TestAgentChatCompressionRenamedToolWithPolicies proves that under
// compression, an opaque renamed MCP tool works correctly with input,
// tool, and output policy gates active. The tool name is "data_probe_v3"
// (renamed from "data_probe") but the schema and results are identical.
//
// Assertions:
//   - Input policy: the user message passes the allow gate.
//   - Tool policy: the MCP tool call is allowed (parent role, read-only).
//   - Output policy: the final response passes the output gate.
//   - Compression fires and preserves the tool group.
//   - No replay: exactly one handler invocation.
func TestAgentChatCompressionRenamedToolWithPolicies(t *testing.T) {
	const want = "Probe result delivered."
	db, err := store.Open(filepath.Join(t.TempDir(), "policies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const convID = "policy-compress"

	// Seed enough history to force compression with the tight window.
	for i := 0; i < 6; i++ {
		if err := db.SaveMessage(convID, "parent", "user", fmt.Sprintf("prior question %d", i), "", "", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveMessage(convID, "parent", "assistant", fmt.Sprintf("prior answer %d — %s", i, strings.Repeat("f", 200)), "", "allow", ""); err != nil {
			t.Fatal(err)
		}
	}

	cfg := staleSearchTestCfg("")
	cfg.LLM.MaxContextTokens = 400 // very tight
	cfg.LLM.MaxResponseTokens = 50

	// Two tool rounds + final answer.
	model := &staleSearchChatter{script: []*llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_p1", Type: "function", Function: llm.ToolCallFunction{Name: "data_probe_v3", Arguments: map[string]any{"depth": 1}}},
		}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_p2", Type: "function", Function: llm.ToolCallFunction{Name: "data_probe_v3", Arguments: map[string]any{"depth": 2}}},
		}},
		{Role: "assistant", Content: want},
	}}

	a := buildStaleSearchAgent(t, db, cfg, model, convID, false)
	pool := mcp.NewPool("", false, false)
	t.Cleanup(pool.StopAll)
	a.pool = pool

	calls := 0
	server := mcpserver.NewMCPServer("probe", "1")
	server.AddTool(mcpgo.NewTool("data_probe_v3", mcpgo.WithReadOnlyHintAnnotation(true)), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		calls++
		return mcpgo.NewToolResultText(fmt.Sprintf("probe depth result %d", calls)), nil
	})
	if err := pool.RegisterInProcessServer("probe-server", server); err != nil {
		t.Fatal(err)
	}

	resp, err := a.Chat(context.Background(), "Run two probe depths.", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != want {
		t.Fatalf("output policy gate: Content = %q, want %q", resp.Content, want)
	}
	if calls != 2 {
		t.Fatalf("tool policy: handler calls = %d, want 2 (no replay)", calls)
	}

	// Input policy: the user message "Run two probe depths." must appear
	// in at least one recorded request.
	found := false
	for _, msgs := range model.recorded {
		for _, m := range msgs {
			if m.Role == "user" && m.Content == "Run two probe depths." {
				found = true
			}
		}
	}
	if !found {
		t.Error("input policy: current user message not found in any request")
	}

	// Wire invariants on the final request.
	finalReq := model.recorded[len(model.recorded)-1]
	assistantIDs := make(map[string]bool)
	resultIDs := make(map[string]bool)
	for _, m := range finalReq {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				assistantIDs[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	for rid := range resultIDs {
		if !assistantIDs[rid] {
			t.Errorf("final request: orphaned result %q", rid)
		}
	}
	for aid := range assistantIDs {
		if !resultIDs[aid] {
			t.Errorf("final request: orphaned call %q", aid)
		}
	}
}
