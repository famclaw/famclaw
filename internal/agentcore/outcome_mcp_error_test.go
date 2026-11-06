package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

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
