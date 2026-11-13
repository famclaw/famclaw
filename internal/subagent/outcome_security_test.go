package subagent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

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
