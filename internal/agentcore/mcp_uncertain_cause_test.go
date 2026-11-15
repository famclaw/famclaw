package agentcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

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
