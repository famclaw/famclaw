package agentcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
	"log"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMCPErrorCannotImpersonateBuiltinSearchClassification(t *testing.T) {
	const canary = "fake-private-search-diagnostic"
	for _, tc := range []struct{ name, payload string }{
		{"exact marker", WebSearchMarkerRateLimited},
		{"marker with private diagnostic", WebSearchMarkerRateLimited + ": " + canary},
	} {
		for _, rebuild := range []bool{false, true} {
			mode := "legacy"
			if rebuild {
				mode = "rebuilt"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				calls := 0
				server := mcpgoserver.NewMCPServer("test", "1")
				server.AddTool(mcpgo.NewTool("web_search"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
					calls++
					return mcpgo.NewToolResultError(tc.payload), nil
				})
				pool := mcp.NewPool("", false, false)
				if err := pool.RegisterInProcessServer("test", server); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(pool.StopAll)
				chatter := &leakChatter{}
				turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "web_search"}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "search", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{}}}})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
				var logs bytes.Buffer
				prior := log.Writer()
				log.SetOutput(&logs)
				defer log.SetOutput(prior)
				err := NewStageToolLoop(ToolLoopDeps{Pool: pool, MaxIterations: 1, RebuildHistory: rebuild, ClientFactory: func(*Turn) llm.Chatter { return chatter }})(context.Background(), turn)
				if err != nil {
					t.Fatal(err)
				}
				if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Outcome != OutcomeFailure || turn.ToolCalls[0].Disposition != DispositionAttempted || turn.ToolCalls[0].Error == nil {
					t.Fatal("expected one attempted application failure")
				}
				if calls != 1 {
					t.Fatalf("MCP executions = %d, want 1", calls)
				}
				sinks := []string{turn.Output, turn.ToolCalls[0].Output, logs.String()}
				if len(chatter.calls) != 1 {
					t.Fatalf("follow-up calls = %d", len(chatter.calls))
				}
				for _, msg := range chatter.calls[0].msgs {
					sinks = append(sinks, msg.Content)
				}
				for _, sink := range sinks {
					if strings.Contains(sink, WebSearchMarkerRateLimited) || strings.Contains(sink, canary) {
						t.Error("untrusted MCP error content reached a consumer")
					}
				}
			})
		}
	}
}

func TestMCPUncertainCauseClasses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"timeout", fmt.Errorf("private diagnostic: %w", context.DeadlineExceeded), " Error class: timeout."},
		{"cancelled", fmt.Errorf("private diagnostic: %w", context.Canceled), " Error class: cancelled."},
		{"refused", fmt.Errorf("private diagnostic: %w", syscall.ECONNREFUSED), " Error class: connection refused."},
		{"unknown", errors.New("private diagnostic"), ""},
		{"text pretending timeout", errors.New("context deadline exceeded"), ""},
		{"text pretending refusal", errors.New("connection refused"), ""},
		{"nil", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MCPUncertainCauseSuffix(tc.cause)
			if got != tc.want || strings.Contains(got, "private diagnostic") {
				t.Fatalf("suffix=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestToolLoopMCPUncertainCauseMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want string
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(context.Background())
			cancel()
			return c, func() {}
		}, "cancelled"},
		{"timeout", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mcpgoserver.NewMCPServer("test", "1")
			srv.AddTool(mcpgo.NewTool("operation"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText("ok"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("test", srv); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.StopAll)
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "one", Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			chatter := &leakChatter{}
			ctx, cancel := tc.ctx()
			defer cancel()
			if err := NewStageToolLoop(ToolLoopDeps{Pool: pool, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }})(ctx, turn); err != nil {
				t.Fatal(err)
			}
			if len(chatter.calls) != 1 {
				t.Fatalf("chatter calls=%d", len(chatter.calls))
			}
			var message string
			for _, m := range chatter.calls[0].msgs {
				if m.Role == "tool" {
					message = m.Content
				}
			}
			if !strings.Contains(message, "outcome uncertain") || !strings.Contains(message, "verify before trying again") || !strings.Contains(message, "Error class: "+tc.want+".") {
				t.Fatalf("tool message=%q", message)
			}
			if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Outcome != OutcomeUnknown {
				t.Fatalf("results=%+v", turn.ToolCalls)
			}
		})
	}
}

func TestOutcomeMCPApplication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure bool
	}{{"failure", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mcpgoserver.NewMCPServer("test", "1")
			srv.AddTool(mcpgo.NewTool("operation"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				if tc.failure {
					return mcpgo.NewToolResultError("synthetic-secret-marker"), nil
				}
				return mcpgo.NewToolResultText("ok-result"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("test", srv); err != nil {
				t.Fatal(err)
			}
			chatter := &leakChatter{}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "id1", Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			if err := NewStageToolLoop(ToolLoopDeps{Pool: pool, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }})(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing result")
			}
			if tc.failure && turn.ToolCalls[0].Error == nil {
				t.Error("MCP application error was recorded as success")
			}
			for _, call := range chatter.calls {
				for _, msg := range call.msgs {
					if tc.failure && strings.Contains(msg.Content, "synthetic-secret-marker") {
						t.Error("error payload leaked to model")
					}
				}
			}
			if !tc.failure && (turn.ToolCalls[0].Error != nil || turn.ToolCalls[0].Output != "ok-result") {
				t.Error("successful result not preserved")
			}
		})
	}
}

func TestOutcomeUncertainCompletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		canceled bool
	}{{"canceled", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mcpgoserver.NewMCPServer("test", "1")
			srv.AddTool(mcpgo.NewTool("operation"), func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				if tc.canceled {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return mcpgo.NewToolResultText("ok"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("test", srv); err != nil {
				t.Fatal(err)
			}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "id", Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			count := 0
			client := &mockChatter{responses: []llm.Message{{Content: "Everything completed."}}, callCount: &count}
			ctx := context.Background()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := NewStageToolLoop(ToolLoopDeps{Pool: pool, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return client }})(ctx, turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing outcome")
			}
			if tc.canceled {
				if turn.ToolCalls[0].Error == nil {
					t.Error("canceled call recorded as success")
				}
				if !strings.Contains(strings.ToLower(turn.Output), "uncertain") {
					t.Error("uncertain outcome claimed as failure or completion")
				}
			} else if turn.ToolCalls[0].Error != nil || turn.Output != "Everything completed." {
				t.Error("successful outcome changed")
			}
		})
	}
}

// TestIsSafeWebSearchMarker_AllConstants: every marker constant is accepted
// by the allowlist. A new WebSearchMarker* that is missing from the
// IsSafeWebSearchMarker switch fails here.
func TestIsSafeWebSearchMarker_AllConstants(t *testing.T) {
	markers := []string{
		WebSearchMarkerHostNotPermitted,
		WebSearchMarkerProviderNotConfigured,
		WebSearchMarkerRateLimited,
		WebSearchMarkerBackendError,
		WebSearchMarkerTimeout,
		WebSearchMarkerBackendUnreachable,
		WebSearchMarkerBackendAuthConfig,
		WebSearchMarkerInvalidResponse,
		WebSearchMarkerUnavailable,
	}
	for _, m := range markers {
		if !IsSafeWebSearchMarker(m) {
			t.Errorf("IsSafeWebSearchMarker(%q) = false, want true — constant missing from the switch", m)
		}
	}
}

// TestToolErrorDisplay: the truthful failure display shared by the parent's
// tool loop and the subagent dispatch. ReadOnly overrides the generic
// warning; a safe web_search marker renders verbatim (and wins over
// ReadOnly, mirroring the tool loop's check order); everything else keeps
// the generic warning.
func TestToolErrorDisplay(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		readOnly bool
		result   string
		want     string
	}{
		{"generic", "builtin__todo", false, "", ToolErrorDisplayChangesMayHaveOccurred},
		{"generic_ignores_unsafe_result", "builtin__web_fetch", false, "searxng: HTTP 500: dial 10.0.0.7:8080", ToolErrorDisplayChangesMayHaveOccurred},
		{"read_only", "builtin__todo", true, "", ToolErrorDisplayReadOnly},
		{"web_search_marker_rate_limited", "builtin__web_search", false, WebSearchMarkerRateLimited, WebSearchMarkerRateLimited},
		{"web_search_marker_unavailable", "builtin__web_search", false, WebSearchMarkerUnavailable, WebSearchMarkerUnavailable},
		{"web_search_marker_wins_over_read_only", "builtin__web_search", true, WebSearchMarkerTimeout, WebSearchMarkerTimeout},
		{"web_search_unsafe_result_falls_back", "builtin__web_search", false, "web_search: rate limited: 429 from 10.0.0.7", ToolErrorDisplayChangesMayHaveOccurred},
		{"web_search_empty_result_falls_back", "builtin__web_search", false, "", ToolErrorDisplayChangesMayHaveOccurred},
		{"non_search_marker_not_rendered", "builtin__web_fetch", false, WebSearchMarkerRateLimited, ToolErrorDisplayChangesMayHaveOccurred},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolErrorDisplay(tc.toolName, tc.readOnly, tc.result); got != tc.want {
				t.Errorf("ToolErrorDisplay(%q, %v, %q) = %q, want %q", tc.toolName, tc.readOnly, tc.result, got, tc.want)
			}
		})
	}
}

// TestIsSafeWebSearchMarker_RejectsUnknown: anything that is not exactly one
// of the fixed markers must fall through to the generic error display —
// including diagnostic text, near-miss rewordings, and case variants.
func TestIsSafeWebSearchMarker_RejectsUnknown(t *testing.T) {
	unknown := []string{
		"",
		"web_search: empty query",                // provider-internal failure, not a marker
		"web_search: unavailable (extra detail)", // marker plus diagnostic text
		"web_search: Unavailable",                // case variant
		"web_search: rate limited: 429 from 10.0.0.7",   // marker plus host
		"Tool reported an error. No changes were made.", // former generic display
		"web_search: provider server error (5xx)",       // sentinel Error() text, not a marker
	}
	for _, s := range unknown {
		if IsSafeWebSearchMarker(s) {
			t.Errorf("IsSafeWebSearchMarker(%q) = true, want false", s)
		}
	}
}
