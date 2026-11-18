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
