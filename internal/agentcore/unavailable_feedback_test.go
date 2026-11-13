package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestUnavailableToolRetryAndHistoryRemainSkipped(t *testing.T) {
	for _, rebuild := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "rebuilt"}[rebuild], func(t *testing.T) {
			const name, secret = "mcp__missing", "fake-private-argument"
			args := map[string]any{"token": secret}
			chatter := &dedupChatter{responses: []llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: args}}}},
				{Role: "assistant", Content: "done"},
			}}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			backendCalls := 0
			deps := ToolLoopDeps{MaxIterations: 3, RebuildHistory: rebuild, ClientFactory: func(*Turn) llm.Chatter { return chatter },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { backendCalls++; return "", nil }}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if backendCalls != 0 || len(turn.ToolCalls) != 2 {
				t.Fatalf("executions=%d results=%d", backendCalls, len(turn.ToolCalls))
			}
			if turn.ToolCalls[0].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[0].Error, ErrUnknownTool) ||
				turn.ToolCalls[1].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) ||
				!errors.Is(turn.ToolCalls[1].Error, ErrUnknownTool) {
				t.Fatal("unavailable/duplicate error or skipped disposition was lost")
			}
			if len(chatter.captured) != 2 {
				t.Fatalf("model calls=%d", len(chatter.captured))
			}
			var text strings.Builder
			for _, message := range chatter.captured[1] {
				if rebuild || message.Role == "tool" && message.ToolCallID == "retry" {
					text.WriteString(message.Content)
				}
			}
			if !strings.Contains(text.String(), "tool unavailable; not run") || strings.Contains(text.String(), "blocked by policy") || strings.Contains(text.String(), secret) {
				t.Errorf("unavailable feedback missing or unsafe: %q", text.String())
			}
		})
	}
}
