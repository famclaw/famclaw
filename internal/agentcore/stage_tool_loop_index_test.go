package agentcore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestToolLoopSnapshotsInvocationArguments(t *testing.T) {
	const name = "builtin__search"
	args := map[string]any{"query": "original", "nested": map[string]any{"value": "before"}}
	chatter := &dedupChatter{}
	calls := 0
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(_ context.Context, _ string, received map[string]any) (string, error) {
		calls++
		received["query"] = "changed"
		received["nested"].(map[string]any)["value"] = "after"
		return "", errors.New("failed")
	}})
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{
		{Function: llm.ToolCallFunction{Name: name, Arguments: args}},
		{Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{"query": "original", "nested": map[string]any{"value": "before"}}}},
	})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
	if got := turn.ToolCalls[0].Args; got["query"] != "original" || got["nested"].(map[string]any)["value"] != "before" {
		t.Errorf("recorded arguments changed: %#v", got)
	}
	if len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
		t.Errorf("retry not suppressed: %+v", turn.ToolCalls)
	}
}

func TestUnsuccessfulCallIndex(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	negativeZero := math.Copysign(0, -1)
	cases := []struct {
		name     string
		previous map[string]any
		query    map[string]any
		match    bool
	}{
		{"JSON object", map[string]any{"nested": []any{"x", float64(1)}}, map[string]any{"nested": []any{"x", float64(1)}}, true},
		{"signed zero", map[string]any{"x": negativeZero}, map[string]any{"x": float64(0)}, true},
		{"number types", map[string]any{"x": 1}, map[string]any{"x": float64(1)}, false},
		{"nil and empty", nil, map[string]any{}, false},
		{"nested nil map and empty map", map[string]any{"x": map[string]any(nil)}, map[string]any{"x": map[string]any{}}, false},
		{"nested nil slice and empty slice", map[string]any{"x": []any(nil)}, map[string]any{"x": []any{}}, false},
		{"custom values", map[string]any{"x": 1}, map[string]any{"x": 1}, true},
		{"cyclic value", cycle, cycle, true},
		{"JSON encoding collision", map[string]any{"x": "\xff"}, map[string]any{"x": "\xfe"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := []ToolResult{{ToolName: "tool", Args: tc.previous, Error: errors.New("first"), Disposition: DispositionPending}, {ToolName: "tool", Args: tc.previous, Error: errors.New("second"), Disposition: DispositionDenied}}
			idx := newUnsuccessfulCallIndex()
			got, ok := idx.find("tool", tc.query, results)
			if ok != tc.match {
				t.Fatalf("match = %v, want %v", ok, tc.match)
			}
			if ok && got.Disposition != DispositionPending {
				t.Errorf("matched disposition %v, want first result", got.Disposition)
			}
		})
	}
}

func BenchmarkUnsuccessfulCallLookup(b *testing.B) {
	for _, n := range []int{2000, 4000, 8000} {
		inputs := make([]map[string]any, n)
		for i := range inputs {
			inputs[i] = map[string]any{"query": fmt.Sprintf("item-%d", i)}
		}
		for _, indexed := range []bool{false, true} {
			label := "scan"
			if indexed {
				label = "index"
			}
			b.Run(fmt.Sprintf("%s/%d", label, n), func(b *testing.B) {
				for iteration := 0; iteration < b.N; iteration++ {
					results := make([]ToolResult, 0, n)
					idx := newUnsuccessfulCallIndex()
					for _, args := range inputs {
						var found bool
						if indexed {
							_, found = idx.find("builtin__search", args, results)
						} else {
							_, found = priorUnsuccessfulCall("builtin__search", args, results)
						}
						if found {
							b.Fatal("unexpected match")
						}
						results = append(results, ToolResult{ToolName: "builtin__search", Args: args, Outcome: OutcomeFailure, Disposition: DispositionAttempted})
					}
				}
			})
		}
	}
}

type customJSONArgument struct{ calls *int }

func (c customJSONArgument) MarshalJSON() ([]byte, error) {
	*c.calls++
	return []byte(`"custom"`), nil
}

func TestUnsuccessfulCallIndexFallbackOrderAndCustomMarshaler(t *testing.T) {
	calls := 0
	args := map[string]any{"custom": customJSONArgument{calls: &calls}}
	results := []ToolResult{{ToolName: "tool", Args: args, Error: errors.New("first"), Disposition: DispositionPending}}
	idx := newUnsuccessfulCallIndex()
	idx.find("other", nil, results)
	args["custom"] = "normal"
	results = append(results, ToolResult{ToolName: "tool", Args: map[string]any{"custom": "normal"}, Error: errors.New("second"), Disposition: DispositionDenied})
	got, ok := idx.find("tool", map[string]any{"custom": "normal"}, results)
	if !ok || got.Disposition != DispositionPending {
		t.Errorf("first fallback result not selected: %+v, %v", got, ok)
	}
	if calls != 0 {
		t.Errorf("custom marshaler called %d times", calls)
	}
}

func TestCallArgsKeyDepthBoundary(t *testing.T) {
	for _, tc := range []struct {
		depth     int
		supported bool
	}{{99, true}, {100, false}, {101, false}} {
		t.Run(fmt.Sprint(tc.depth), func(t *testing.T) {
			var value any = "end"
			for i := 0; i < tc.depth; i++ {
				value = []any{value}
			}
			_, ok := callArgsKey(map[string]any{"value": value})
			// The root map occupies depth zero, so the leaf is one level deeper.
			if ok != tc.supported {
				t.Errorf("depth %d: supported %v", tc.depth, ok)
			}
		})
	}
}

func TestUnsuccessfulCallIndexMatchesScan(t *testing.T) {
	values := []map[string]any{nil, {}, {"x": float64(1)}, {"x": 1}, {"x": "\xff"}, {"x": "\xfe"}, {"x": []any{map[string]any(nil)}}}
	idx := newUnsuccessfulCallIndex()
	var results []ToolResult
	for i, args := range values {
		results = append(results, ToolResult{ToolName: "tool", Args: args, Disposition: DispositionAttempted, Outcome: OutcomeSuccess})
		results = append(results, ToolResult{ToolName: "tool", Args: args, Error: errors.New("failure"), Disposition: DispositionDenied + ToolDisposition(i%3)})
		for _, query := range values {
			want, wantOK := priorUnsuccessfulCall("tool", query, results)
			got, gotOK := idx.find("tool", query, results)
			if gotOK != wantOK || (gotOK && got.Disposition != want.Disposition) {
				t.Fatalf("after %d results, query %#v: index = %+v, %v; scan = %+v, %v", len(results), query, got, gotOK, want, wantOK)
			}
		}
	}
}
