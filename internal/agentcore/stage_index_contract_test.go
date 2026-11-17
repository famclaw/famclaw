package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"math"
	"strings"
	"testing"
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

func TestUnsuccessfulCallIndexSnapshotIsolation(t *testing.T) {
	original := map[string]any{"nested": map[string]any{"value": "before"}}
	results := []ToolResult{{ToolName: "tool", Args: original, Error: errors.New("failed"), Disposition: DispositionAttempted, Outcome: OutcomeFailure}}
	idx := newUnsuccessfulCallIndex()
	if _, found := idx.find("tool", map[string]any{"nested": map[string]any{"value": "before"}}, results); !found {
		t.Fatal("original arguments did not match")
	}
	results[0].Args["nested"].(map[string]any)["value"] = "result changed"
	if got := original["nested"].(map[string]any)["value"]; got != "before" {
		t.Errorf("result mutation changed external argument: %v", got)
	}
	original["nested"].(map[string]any)["value"] = "external changed"
	if got := results[0].Args["nested"].(map[string]any)["value"]; got != "result changed" {
		t.Errorf("external mutation changed result snapshot: %v", got)
	}
}

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
