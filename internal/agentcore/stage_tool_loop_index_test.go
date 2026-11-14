package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
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

func TestToolLoopSignedZeroRetry(t *testing.T) {
	const name = "builtin__search"
	for _, firstNegative := range []bool{false, true} {
		for _, fails := range []bool{false, true} {
			t.Run(fmt.Sprintf("firstNegative=%t/fails=%t", firstNegative, fails), func(t *testing.T) {
				failure := errors.New("failed")
				decode := func(negative bool) map[string]any {
					raw := `{"nested":[{"x":0.0}]}`
					if negative {
						raw = `{"nested":[{"x":-0.0}]}`
					}
					var args map[string]any
					if err := json.Unmarshal([]byte(raw), &args); err != nil {
						t.Fatal(err)
					}
					return args
				}
				sign := func(args map[string]any) bool {
					return math.Signbit(args["nested"].([]any)[0].(map[string]any)["x"].(float64))
				}
				var signs []bool
				stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return &dedupChatter{} }, BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
					signs = append(signs, sign(args))
					if fails {
						return "", failure
					}
					return "ok", nil
				}})
				turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{
					{Function: llm.ToolCallFunction{Name: name, Arguments: decode(firstNegative)}},
					{Function: llm.ToolCallFunction{Name: name, Arguments: decode(!firstNegative)}},
				})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
				if err := stage(context.Background(), turn); err != nil {
					t.Fatal(err)
				}
				if len(turn.ToolCalls) != 2 {
					t.Fatalf("recorded calls = %d, want 2", len(turn.ToolCalls))
				}
				if got := sign(turn.ToolCalls[0].Args); got != firstNegative {
					t.Errorf("first recorded sign = %t, want %t", got, firstNegative)
				}
				wantCalls := 2
				if fails {
					wantCalls = 1
				}
				if len(signs) != wantCalls {
					t.Fatalf("handler calls = %d, want %d", len(signs), wantCalls)
				}
				if signs[0] != firstNegative || (!fails && signs[1] == firstNegative) {
					t.Errorf("handler signs = %v", signs)
				}
				if got := errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed); got != fails {
					t.Errorf("retry suppressed = %t, want %t", got, fails)
				}
				first, second := turn.ToolCalls[0], turn.ToolCalls[1]
				if fails {
					if !errors.Is(first.Error, failure) || first.Outcome != OutcomeFailure || first.Disposition != DispositionAttempted || second.Disposition != DispositionSkipped || second.Outcome != OutcomeUnknown {
						t.Errorf("failed call and retry: %+v, %+v", first, second)
					}
				} else if first.Error != nil || second.Error != nil || first.Outcome != OutcomeSuccess || second.Outcome != OutcomeSuccess || first.Disposition != DispositionAttempted || second.Disposition != DispositionAttempted {
					t.Errorf("successful calls: %+v, %+v", first, second)
				}
			})
		}
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
	dispositions := [...]ToolDisposition{DispositionDenied, DispositionPending, DispositionSkipped}
	idx := newUnsuccessfulCallIndex()
	var results []ToolResult
	for i, args := range values {
		results = append(results, ToolResult{ToolName: "tool", Args: args, Disposition: DispositionAttempted, Outcome: OutcomeSuccess})
		results = append(results, ToolResult{ToolName: "tool", Args: args, Error: errors.New("failure"), Disposition: dispositions[i%len(dispositions)]})
		for _, query := range values {
			want, wantOK := priorUnsuccessfulCall("tool", query, results)
			got, gotOK := idx.find("tool", query, results)
			if gotOK != wantOK || (gotOK && got.Disposition != want.Disposition) {
				t.Fatalf("after %d results, query %#v: index = %+v, %v; scan = %+v, %v", len(results), query, got, gotOK, want, wantOK)
			}
		}
	}
}

func TestCallArgsKeyCopyBudget(t *testing.T) {
	shared := map[string]any{"v": true}
	for i := 0; i < 14; i++ {
		shared = map[string]any{"a": shared, "b": shared}
	}
	atNodeLimit := make(map[string]any, 16383)
	for i := 0; i < 16383; i++ {
		atNodeLimit[fmt.Sprint(i)] = nil
	}
	aboveNodeLimit := make(map[string]any, 16384)
	for i := 0; i < 16384; i++ {
		aboveNodeLimit[fmt.Sprint(i)] = nil
	}
	cases := []struct {
		name    string
		args    map[string]any
		indexed bool
	}{
		{"shared graph", shared, false},
		{"at node limit", atNodeLimit, true},
		{"above node limit", aboveNodeLimit, false},
		{"at string byte limit", map[string]any{"": strings.Repeat("x", 1<<20)}, true},
		{"above string byte limit", map[string]any{"": strings.Repeat("x", (1<<20)+1)}, false},
		{"above key byte limit", map[string]any{strings.Repeat("x", (1<<20)+1): nil}, false},
		{"key and value share byte budget", map[string]any{strings.Repeat("k", 1<<19): strings.Repeat("v", (1<<19)+1)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := callArgsKey(tc.args)
			if ok != tc.indexed {
				t.Errorf("indexed = %v, want %v", ok, tc.indexed)
			}
		})
	}
}

func TestCallArgsKeyEscapedBytesBounded(t *testing.T) {
	key := strings.Repeat("\x00", 10000)
	encoded, ok := callArgsKey(map[string]any{key: "value"})
	if !ok {
		t.Fatal("ordinary escaped key unexpectedly rejected")
	}
	if len(encoded) > 6*(1<<20)+32*16384 {
		t.Fatalf("encoded size %d exceeds conservative bound", len(encoded))
	}
}

func TestUnsuccessfulCallIndexOversizedFallbackOrder(t *testing.T) {
	oversized := map[string]any{"payload": strings.Repeat("x", (1<<20)+1)}
	results := []ToolResult{{ToolName: "tool", Args: oversized, Error: errors.New("first"), Disposition: DispositionPending}, {ToolName: "tool", Args: oversized, Error: errors.New("second"), Disposition: DispositionDenied}}
	idx := newUnsuccessfulCallIndex()
	got, ok := idx.find("tool", oversized, results)
	if !ok || got.Disposition != DispositionPending {
		t.Errorf("fallback did not preserve first result: %+v %v", got, ok)
	}
	if len(idx.fallback) != 2 {
		t.Errorf("fallback entries = %d, want 2", len(idx.fallback))
	}
}

func TestToolLoopOversizedArgumentsUseFallback(t *testing.T) {
	const name = "builtin__search"
	args := map[string]any{"query": strings.Repeat("x", (1<<20)+1)}
	chatter := &dedupChatter{}
	calls := 0
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(_ context.Context, _ string, _ map[string]any) (string, error) {
		calls++
		return "", errors.New("failed")
	}})
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{
		{Function: llm.ToolCallFunction{Name: name, Arguments: args}},
		{Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{"query": args["query"]}}},
	})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
		t.Errorf("calls = %d, results = %+v", calls, turn.ToolCalls)
	}
}

// Different wire representations of no arguments must suppress the same failed retry.
func TestToolLoopDeduplicatesDecodedEmptyArguments(t *testing.T) {
	const name = "builtin__search"
	var first, retry llm.ToolCallArguments
	if err := json.Unmarshal([]byte(`null`), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`"null"`), &retry); err != nil {
		t.Fatal(err)
	}
	chatter := &dedupChatter{}
	calls := 0
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(_ context.Context, _ string, _ map[string]any) (string, error) {
		calls++
		return "", errors.New("failed")
	}})
	turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{Function: llm.ToolCallFunction{Name: name, Arguments: first}}, {Function: llm.ToolCallFunction{Name: name, Arguments: retry}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
		t.Errorf("handler calls = %d; retry was not deduplicated", calls)
	}
}
