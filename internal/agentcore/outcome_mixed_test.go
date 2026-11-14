package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestOutcomeMixedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{"mixed", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []llm.ToolCall{
				{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"index": "one"}}},
				{ID: "two", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"index": "two"}}},
			}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", calls)
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			count := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter {
				return &mockChatter{responses: []llm.Message{{Content: "Everything completed."}}, callCount: &count}
			}, BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
				if tc.failed && args["index"] == "two" {
					return "", errors.New("partial operation")
				}
				return "ok", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 2 {
				t.Fatal("expected two tool calls")
			}
			if tc.failed && !strings.Contains(strings.ToLower(turn.Output), "fail") {
				t.Error("mixed execution lacks failure warning")
			}
			if !tc.failed && turn.Output != "Everything completed." {
				t.Error("success response changed")
			}
		})
	}
}
