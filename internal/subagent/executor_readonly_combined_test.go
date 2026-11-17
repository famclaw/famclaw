package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
// must get the declaration-aware display, and a failing
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
		{"read-only MCP failure reports its declaration", "readonly_lookup", agentcore.ToolErrorDisplayReadOnly},
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
// failure must report the read-only declaration, and a
// mutating MCP tool failure must keep the conservative display.
func TestSubagentExecuteMCPReadOnlyFailureDisplay(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want string
	}{
		{"readonly_lookup", agentcore.ToolErrorDisplayReadOnly},
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

// A server-provided read-only hint does not prove that the handler had no effects.
func TestSubagentMCPMisdeclaredReadOnlyFailure(t *testing.T) {
	var changed atomic.Bool
	srv := mcpgoserver.NewMCPServer("misdeclared", "1")
	srv.AddTool(mcpgo.NewTool("operation", mcpgo.WithReadOnlyHintAnnotation(true)),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			changed.Store(true)
			return mcpgo.NewToolResultError("failed"), nil
		})
	pool := mcp.NewPool("", false, false)
	if err := pool.RegisterInProcessServer("misdeclared", srv); err != nil {
		t.Fatal(err)
	}
	defer pool.StopAll()
	_, allowed, readOnly := filterTools(pool.ListToolInfos(), []string{"operation"}, nil)
	call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}
	got, ok := dispatchSubagentTool(context.Background(), call, ExecutorDeps{Pool: pool}, allowed, nil, readOnly, time.Now())
	if !ok || !changed.Load() {
		t.Fatalf("handler did not run and change state: dispatched=%v changed=%v", ok, changed.Load())
	}
	if !strings.Contains(strings.ToLower(got), "marked read-only") || strings.Contains(strings.ToLower(got), "no changes") {
		t.Errorf("misleading tool result: %q", got)
	}
}

// TestSubagentReadOnlyBareNameLookup proves dispatchSubagentTool's per-call
// read-only classification is correct in every name form that can reach it:
// the namespaced registry form ("builtin__web_search") and the bare form a
// local LLM may emit instead ("web_search"). Regression: BuiltinReadOnly was
// once keyed only on the namespaced tool name in agent.handleSpawnAgent, so a
// bare-form lookup silently missed and a read-only tool failure degraded to
// the conservative "some changes may have occurred" display instead of "No
// changes were made."
func TestSubagentReadOnlyBareNameLookup(t *testing.T) {
	// Build the map exactly the way agent.handleSpawnAgent keys it: both
	// the namespaced registry name and its bare form, for ReadOnly tools
	// only (mutating tools are absent, staying potentially mutating).
	registry := []struct {
		name     string
		readOnly bool
	}{
		{"builtin__web_search", true},
		{"builtin__web_fetch", true},
		{"builtin__todo", false},
	}
	readonly := make(map[string]bool, len(registry)*2)
	for _, tool := range registry {
		if tool.readOnly {
			readonly[tool.name] = true
			readonly[strings.TrimPrefix(tool.name, "builtin__")] = true
		}
	}

	for _, tc := range []struct {
		name    string
		emitted string
		want    string
	}{
		{"bare read-only web_search", "web_search", agentcore.ToolErrorDisplayReadOnly},
		{"bare read-only web_fetch", "web_fetch", agentcore.ToolErrorDisplayReadOnly},
		{"namespaced read-only web_search", "builtin__web_search", agentcore.ToolErrorDisplayReadOnly},
		{"bare mutating tool stays conservative", "todo", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
		{"namespaced mutating tool stays conservative", "builtin__todo", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := ExecutorDeps{BuiltinReadOnly: readonly}
			deps.BuiltinHandler = func(context.Context, string, map[string]any) (string, error) {
				return "", errors.New("boom")
			}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: tc.emitted, Arguments: map[string]any{}}}
			// The allowlist gate (filterBuiltinDefs) is a separate mechanism;
			// here it is opened for the emitted name so the read-only
			// classification lookup itself is what is under test.
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, map[string]bool{tc.emitted: true}, map[string]bool{}, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if result != tc.want {
				t.Errorf("display = %q, want %q", result, tc.want)
			}
		})
	}
}

// TestSubagentBareNameNormalizedForToolErrorDisplay proves a bare-form
// "web_search" emitted by the LLM is normalized to the namespaced registry
// form before dispatch and before ToolErrorDisplay. Regression: dispatch
// had no bare-name normalization step (unlike the main tool loop), so with
// an allowlist keyed on the namespaced name the bare call was rejected as
// an unknown tool, and a bare web_search failure could never reach
// ToolErrorDisplay's builtin__web_search-specific safe-marker branch.
func TestSubagentBareNameNormalizedForToolErrorDisplay(t *testing.T) {
	marker := agentcore.WebSearchMarkerUnavailable
	var dispatchedAs string
	deps := ExecutorDeps{
		// Keyed on both forms, exactly as agent.handleSpawnAgent builds it.
		BuiltinReadOnly: map[string]bool{"builtin__web_search": true, "web_search": true},
	}
	deps.BuiltinHandler = func(_ context.Context, name string, _ map[string]any) (string, error) {
		dispatchedAs = name
		return marker, errors.New("boom")
	}

	call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{}}}
	// The allowlist is keyed on the namespaced registry name, exactly as
	// filterBuiltinDefs produces it from the parent's namespaced defs.
	allowedBuiltins := map[string]bool{"builtin__web_search": true}
	result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, allowedBuiltins, map[string]bool{}, time.Now())
	if !ok {
		t.Fatal("bare web_search was not dispatched to the builtin handler; want it normalized and allowed")
	}
	if dispatchedAs != "builtin__web_search" {
		t.Errorf("handler dispatched as %q, want normalized %q", dispatchedAs, "builtin__web_search")
	}
	if result != marker {
		t.Errorf("display = %q, want specific web_search marker %q", result, marker)
	}
}

// TestSubagentToolErrorDisplay: the subagent's dispatch must apply the same
// truthful failure display as the parent's tool loop
// (agentcore.ToolErrorDisplay): ReadOnly tools report no changes, a
// web_search failure renders its safe classification marker verbatim,
// and everything else keeps the generic "changes may have occurred"
// warning.
func TestSubagentToolErrorDisplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		readOnly bool
		out      string
		want     string
	}{
		{"builtin_generic", "builtin__operation", false, "", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
		{"builtin_read_only", "builtin__operation", true, "", agentcore.ToolErrorDisplayReadOnly},
		{"builtin_web_search_marker", "builtin__web_search", false, agentcore.WebSearchMarkerRateLimited, agentcore.WebSearchMarkerRateLimited},
		{"builtin_web_search_unsafe_result", "builtin__web_search", false, "searxng: HTTP 500 from 10.0.0.7", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readonly := map[string]bool{}
			if tc.readOnly {
				readonly[tc.tool] = true
			}
			deps := ExecutorDeps{BuiltinReadOnly: readonly}
			deps.BuiltinHandler = func(context.Context, string, map[string]any) (string, error) {
				return tc.out, errors.New("boom")
			}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: tc.tool, Arguments: map[string]any{}}}
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, map[string]bool{tc.tool: true}, map[string]bool{}, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if result != tc.want {
				t.Errorf("display = %q, want %q", result, tc.want)
			}
		})
	}
}

func TestSubagentBuiltinReadOnlyMetadata(t *testing.T) {
	const name = "builtin__web_search"
	for _, tc := range []struct {
		name     string
		metadata map[string]bool
		want     string
	}{
		{"unknown", nil, agentcore.ToolErrorDisplayChangesMayHaveOccurred},
		{"declared read-only", map[string]bool{name: true}, agentcore.ToolErrorDisplayReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := ExecutorDeps{BuiltinReadOnly: tc.metadata, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				return "", errors.New("failed")
			}}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: name}}
			got, ok := dispatchSubagentTool(context.Background(), call, deps, nil, map[string]bool{name: true}, nil, time.Now())
			if !ok || got != tc.want {
				t.Errorf("display = %q, dispatched = %t; want %q, true", got, ok, tc.want)
			}
		})
	}
}

func TestOutcomeSubagentDispatch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		builtin, fail bool
	}{
		{"builtin_failure", true, true}, {"mcp_failure", false, true}, {"builtin_success", true, false}, {"mcp_success", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "fake-*" fixture: synthetic test marker, not a live secret. The
			// check-private-details guard (CREDENTIAL shape) only exempts the
			// documented placeholder conventions (fake-*/test-*/changeme/...),
			// so keep this value inside that allowlist.
			const secret = "fake-subagent-secret"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			name := "operation"
			deps := ExecutorDeps{}
			if tc.builtin {
				name = "builtin__operation"
				deps.BuiltinHandler = func(context.Context, string, map[string]any) (string, error) {
					if tc.fail {
						return "", errors.New(secret)
					}
					return "safe-success", nil
				}
			} else {
				srv := mcpgoserver.NewMCPServer("test", "1")
				srv.AddTool(mcpgo.NewTool(name), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
					if tc.fail {
						return mcpgo.NewToolResultError(secret), nil
					}
					return mcpgo.NewToolResultText("safe-success"), nil
				})
				deps.Pool = mcp.NewPool("", false, false)
				if err := deps.Pool.RegisterInProcessServer("test", srv); err != nil {
					t.Fatal(err)
				}
			}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{}}}
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{name: !tc.builtin}, map[string]bool{name: tc.builtin}, map[string]bool{}, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if tc.fail {
				if strings.Contains(result, secret) || strings.Contains(logs.String(), secret) {
					t.Error("private cause leaked")
				}
				if !strings.Contains(strings.ToLower(result), "error") {
					t.Error("failure not reported")
				}
				if strings.Contains(logs.String(), "tool_ok") {
					t.Error("failure recorded as success")
				}
			} else if !strings.Contains(result, "safe-success") {
				t.Error("success not preserved")
			}
		})
	}
}
