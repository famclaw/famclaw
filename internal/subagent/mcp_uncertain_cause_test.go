package subagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestSubagentMCPUncertainCauseMessage(t *testing.T) {
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
			srv := server.NewMCPServer("test", "1")
			srv.AddTool(protocol.NewTool("operation"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
				return protocol.NewToolResultText("ok"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("test", srv); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.StopAll)
			ctx, cancel := tc.ctx()
			defer cancel()
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}
			got, ok := dispatchSubagentTool(ctx, call, ExecutorDeps{Pool: pool}, map[string]bool{"operation": true}, nil, nil, time.Now())
			if !ok || !strings.Contains(got, "outcome uncertain") || !strings.Contains(got, "Verify before trying again") || !strings.Contains(got, "Error class: "+tc.want+".") {
				t.Fatalf("dispatch=(%q,%v)", got, ok)
			}
		})
	}
}
