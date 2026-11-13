package agentcore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestLoopDetectorUsesInvocationArguments(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			stub := newStubChatterClickLoop(6)
			calls := 0
			deps := ToolLoopDeps{ClientFactory: func(*Turn) llm.Chatter { return stub }, MaxIterations: 20, RebuildHistory: true, EnableLoopDetector: enabled, BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
				calls++
				args["ref"] = fmt.Sprintf("mutated-%d", calls)
				return "ok", nil
			}}
			turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__browser_click"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: "builtin__browser_click", Arguments: map[string]any{"ref": "e1"}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "keep clicking"}})
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != 7 || len(turn.ToolCalls) != 7 {
				t.Fatalf("handler calls=%d, results=%d; want 7 each", calls, len(turn.ToolCalls))
			}
			for _, r := range turn.ToolCalls {
				if r.Args["ref"] != "e1" || r.Outcome != OutcomeSuccess || r.Error != nil {
					t.Fatalf("unexpected result: %+v", r)
				}
			}
			saw := false
			for _, batch := range stub.captured {
				for _, m := range batch {
					if m.Role == "user" && strings.Contains(m.Content, "If this repetition is intentional") {
						saw = true
					}
				}
			}
			if saw != enabled {
				t.Errorf("nudge seen=%t, want %t", saw, enabled)
			}
		})
	}
}
