package agentcore

import (
	"context"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"strings"
	"testing"
)

func TestOutcomeCapPendingCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  bool
	}{{"cap", true}, {"stopped", false}} {
		t.Run(tc.name, func(t *testing.T) {
			call := llm.ToolCall{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{}}}
			reply := llm.Message{Content: "Everything is complete."}
			if tc.cap {
				reply.ToolCalls = []llm.ToolCall{call}
			}
			count := 0
			execs := 0
			chatter := &mockChatter{responses: []llm.Message{reply}, callCount: &count}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{call})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { execs++; return "ok", nil }}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if execs != 1 {
				t.Fatalf("got %d executions; want 1", execs)
			}
			if tc.cap && (!strings.Contains(strings.ToLower(turn.Output), "not run") || !strings.Contains(strings.ToLower(turn.Output), "limit")) {
				t.Error("cap response failed to disclose unexecuted request")
			}
			if tc.cap && !strings.Contains(strings.ToLower(turn.Output), "everything is complete.") {
				t.Errorf("cap hit must preserve the model's last response text, got %q", turn.Output)
			}
			if !tc.cap && turn.Output != "Everything is complete." {
				t.Error("stopped success changed")
			}
		})
	}
}
