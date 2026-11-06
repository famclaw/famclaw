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
