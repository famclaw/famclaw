package subagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

// newMCPReadonlyFailurePool registers an in-process MCP server with a
// read-only tool (readOnlyHint=true) and a mutating tool, both of which
// fail with IsError results. This is the fixture for the MCP read-only
// failure-display tests below.
func newMCPReadonlyFailurePool(t *testing.T) *mcp.Pool {
	t.Helper()
	srv := mcpgoserver.NewMCPServer("readonly-fixture", "1")
	srv.AddTool(mcpgo.NewTool("readonly_lookup", mcpgo.WithReadOnlyHintAnnotation(true)),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("lookup failed"), nil
		})
	srv.AddTool(mcpgo.NewTool("mutating_write"),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("write failed"), nil
		})
	pool := mcp.NewPool("", false, false)
	if err := pool.RegisterInProcessServer("readonly-fixture", srv); err != nil {
		t.Fatalf("RegisterInProcessServer: %v", err)
	}
	return pool
}

// TestSubagentMCPReadOnlyToolErrorDisplay: a failing read-only MCP tool
// must get the specific "no changes were made" display, and a failing
// mutating MCP tool must keep the conservative "changes may have
// occurred" display. Regression: dispatchSubagentTool classified every
// MCP tool through deps.BuiltinReadOnly, which only ever contains
// BUILTIN tool names, so a read-only MCP tool failure always degraded
// to the conservative display.
func TestSubagentMCPReadOnlyToolErrorDisplay(t *testing.T) {
	pool := newMCPReadonlyFailurePool(t)
	cfg := Config{Tools: []string{"readonly_lookup", "mutating_write"}}
	// Build the maps exactly the way Execute does.
	_, allowedTools, mcpReadOnly := filterTools(pool.ListToolInfos(), cfg.Tools, nil)

	for _, tc := range []struct {
		name string
		tool string
		want string
	}{
		{"read-only MCP failure reports no changes", "readonly_lookup", agentcore.ToolErrorDisplayNoChanges},
		{"mutating MCP failure stays conservative", "mutating_write", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := ExecutorDeps{Pool: pool}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: tc.tool, Arguments: map[string]any{}}}
			result, ok := dispatchSubagentTool(context.Background(), call, deps, allowedTools, map[string]bool{}, mcpReadOnly, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if result != tc.want {
				t.Errorf("display = %q, want %q", result, tc.want)
			}
		})
	}
}

// TestSubagentExecuteMCPReadOnlyFailureDisplay proves the full Execute
// wiring (pool -> ListToolInfos -> filterTools -> dispatch): the
// tool-role message a subagent's LLM sees after a read-only MCP tool
// failure must be the specific "no changes were made" display, and a
// mutating MCP tool failure must keep the conservative display.
func TestSubagentExecuteMCPReadOnlyFailureDisplay(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want string
	}{
		{"readonly_lookup", agentcore.ToolErrorDisplayNoChanges},
		{"mutating_write", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			pool := newMCPReadonlyFailurePool(t)
			turn := 0
			var capturedToolContent string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn++
				if turn == 1 {
					// First LLM call: emit the tool call.
					msg := map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      tc.tool,
								"arguments": map[string]any{},
							},
						}},
					}
					resp := map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": "tool_calls"}}}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				// Follow-up call: capture the tool-role message the
				// executor sent back to the model.
				var req struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				for _, m := range req.Messages {
					if m.Role == "tool" {
						capturedToolContent = m.Content
					}
				}
				msg := map[string]any{"role": "assistant", "content": "final answer"}
				resp := map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": "stop"}}}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			cfg := Config{Prompt: "use the tool", LLMProfile: "test-local", MaxTurns: 5, Tools: []string{tc.tool}}
			deps := ExecutorDeps{Config: testConfig(server.URL), Pool: pool, MaxTokens: 100}

			out, err := Execute(context.Background(), cfg, deps)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out != "final answer" {
				t.Errorf("output = %q, want final answer", out)
			}
			if capturedToolContent != tc.want {
				t.Errorf("tool-role display = %q, want %q", capturedToolContent, tc.want)
			}
		})
	}
}
