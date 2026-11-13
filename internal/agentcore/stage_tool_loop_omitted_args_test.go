package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestToolLoopNormalizesOmittedTopLevelArguments(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		wantCalls           int
	}{
		{"omitted then empty", "", `,"arguments":"{}"`, 1},
		{"empty then omitted", `,"arguments":"{}"`, "", 1},
		{"omitted then null", "", `,"arguments":null`, 1},
		{"different nonempty", `,"arguments":{"x":1}`, `,"arguments":{"x":2}`, 2},
		{"nested null versus empty", `,"arguments":{"x":null}`, `,"arguments":{"x":{}}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []llm.ToolCall
			raw := fmt.Sprintf(`[{"id":"one","function":{"name":"builtin__operation"%s}},{"id":"two","function":{"name":"builtin__operation"%s}}]`, tc.first, tc.second)
			if err := json.Unmarshal([]byte(raw), &calls); err != nil {
				t.Fatal(err)
			}
			invocations := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return &dedupChatter{} },
				BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
					invocations++
					if args == nil {
						t.Error("handler received nil top-level arguments")
					}
					return "", errors.New("ambiguous failure")
				}}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", calls)
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if invocations != tc.wantCalls {
				t.Errorf("handler invoked %d times, want %d", invocations, tc.wantCalls)
			}
			if len(turn.ToolCalls) != 2 {
				t.Fatalf("recorded %d tool results, want 2", len(turn.ToolCalls))
			}
			if suppressed := errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed); suppressed != (tc.wantCalls == 1) {
				t.Errorf("second-call suppression = %v, want %v", suppressed, tc.wantCalls == 1)
			}
		})
	}
}
