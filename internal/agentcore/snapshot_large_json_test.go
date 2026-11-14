package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestToolLoopSnapshotsLargeJSONArguments(t *testing.T) {
	const name = "builtin__search"
	large := strings.Repeat("x", (1<<20)+1)
	for _, tc := range []struct{ name, key, value string }{
		{"large string", "payload", large},
		{"large key", large, "payload"},
	} {
		for _, fail := range []bool{false, true} {
			label := "success"
			if fail {
				label = "failure"
			}
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				data, err := json.Marshal(map[string]any{tc.key: tc.value, "mode": "original"})
				if err != nil {
					t.Fatal(err)
				}
				decode := func() map[string]any {
					var args map[string]any
					if err := json.Unmarshal(data, &args); err != nil {
						t.Fatal(err)
					}
					return args
				}
				args := decode()
				if _, ok := callArgsKey(args); ok {
					t.Fatal("large argument unexpectedly indexable")
				}
				failure := errors.New("failed")
				calls := 0
				stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return &dedupChatter{} }, BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
					calls++
					args["mode"] = "mutated"
					if fail {
						return "", failure
					}
					return "ok", nil
				}})
				turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{{Function: llm.ToolCallFunction{Name: name, Arguments: args}}, {Function: llm.ToolCallFunction{Name: name, Arguments: decode()}}})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
				if err := stage(context.Background(), turn); err != nil {
					t.Fatal(err)
				}
				if len(turn.ToolCalls) != 2 {
					t.Fatalf("recorded calls=%d", len(turn.ToolCalls))
				}
				want := 2
				if fail {
					want = 1
				}
				if calls != want {
					t.Errorf("handler calls=%d, want %d", calls, want)
				}
				if got := turn.ToolCalls[0].Args["mode"]; got != "original" {
					t.Errorf("recorded mode=%v, want original", got)
				}
				if got := errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed); got != fail {
					t.Errorf("retry suppressed=%t, want %t", got, fail)
				}
				if fail && (!errors.Is(turn.ToolCalls[0].Error, failure) || turn.ToolCalls[0].Outcome != OutcomeFailure || turn.ToolCalls[1].Disposition != DispositionSkipped) {
					t.Error("incorrect failed/retry metadata")
				}
				if !fail && (turn.ToolCalls[0].Outcome != OutcomeSuccess || turn.ToolCalls[1].Outcome != OutcomeSuccess) {
					t.Error("incorrect success metadata")
				}
			})
		}
	}
}
