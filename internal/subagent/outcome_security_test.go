package subagent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

func TestOutcomeSubagentDispatch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		builtin, fail bool
	}{
		{"builtin_failure", true, true}, {"mcp_failure", false, true}, {"builtin_success", true, false}, {"mcp_success", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "synthetic-subagent-secret"
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
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{name: !tc.builtin}, map[string]bool{name: tc.builtin}, time.Now())
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
