package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestSameBatchDedupIndexesNewDenialsAndSkips(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tool        string
		allowed     []Tool
		first       ToolDisposition
		wantMessage string
	}{
		{"allowlist denial", "builtin__denied", []Tool{{Name: "builtin__other"}}, DispositionDenied, "not in the turn allowlist"},
		{"unknown tool skip", "unknown_tool", []Tool{{Name: "unknown_tool"}}, DispositionSkipped, "tool unavailable; not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatter := &dedupChatter{responses: []llm.Message{{Role: "assistant", Content: "done"}}}
			calls := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: tc.allowed}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{
				{ID: "first", Function: llm.ToolCallFunction{Name: tc.tool, Arguments: map[string]any{"x": "same"}}},
				{ID: "duplicate", Function: llm.ToolCallFunction{Name: tc.tool, Arguments: map[string]any{"x": "same"}}},
				{ID: "different", Function: llm.ToolCallFunction{Name: tc.tool, Arguments: map[string]any{"x": "different"}}},
			})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1,
				ClientFactory:  func(*Turn) llm.Chatter { return chatter },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil },
			})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != 0 || len(turn.ToolCalls) != 3 {
				t.Fatalf("calls=%d, results=%+v", calls, turn.ToolCalls)
			}
			if turn.ToolCalls[0].Disposition != tc.first || turn.ToolCalls[0].Error == nil {
				t.Fatalf("first=%+v", turn.ToolCalls[0])
			}
			if turn.ToolCalls[1].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
				t.Fatalf("duplicate=%+v", turn.ToolCalls[1])
			}
			if turn.ToolCalls[2].Disposition != tc.first || errors.Is(turn.ToolCalls[2].Error, ErrToolAlreadyFailed) {
				t.Fatalf("different args=%+v", turn.ToolCalls[2])
			}
			var found bool
			for _, batch := range chatter.captured {
				for _, msg := range batch {
					if msg.Role == "tool" && msg.ToolCallID == "duplicate" && strings.Contains(msg.Content, tc.wantMessage) {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("missing duplicate feedback: %+v", chatter.captured)
			}
		})
	}
}
