package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// TestTurnReadOnlyLookupBothForms proves the per-call read-only lookup
// classifies a tool identically in every name form that can reach the
// loop: the namespaced registry form ("builtin__web_search",
// "mcp__server__tool") and the bare form the LLM was advertised
// ("web_search", "tool"). Regression: turnReadOnly was once keyed only on
// t.Name, so a bare-form lookup silently missed and the per-call
// ToolErrorDisplay degraded to the conservative "changes may have
// occurred" while outcomeWarning still reported read-only correctly.
func TestTurnReadOnlyLookupBothForms(t *testing.T) {
	// Build the map exactly the way the tool loop populates it:
	// both the registry name and its bare form, for ReadOnly tools only.
	tools := []Tool{
		{Name: "builtin__web_search", ReadOnly: true},
		{Name: "builtin__web_fetch", ReadOnly: true},
		{Name: "mcp__weather__forecast", ReadOnly: true},
		{Name: "builtin__file_write"}, // mutating
		{Name: "builtin__todo"},       // mutating
	}
	set := make(map[string]bool, len(tools)*2)
	for _, t := range tools {
		if t.ReadOnly {
			set[t.Name] = true
			set[bareToolName(t.Name)] = true
		}
	}

	cases := []struct {
		name       string
		probedName string
		want       bool
	}{
		{"builtin namespaced form", "builtin__web_search", true},
		{"builtin bare form", "web_search", true},
		{"builtin bare form second", "web_fetch", true},
		{"mcp namespaced form", "mcp__weather__forecast", true},
		{"mcp bare form", "forecast", true},
		{"mutating tool namespaced", "builtin__file_write", false},
		{"mutating tool bare", "file_write", false},
		{"mutating tool namespaced second", "builtin__todo", false},
		{"unknown name", "nonexistent_tool", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := turnReadOnlyLookup(set, tc.probedName); got != tc.want {
				t.Errorf("turnReadOnlyLookup(set, %q) = %v, want %v", tc.probedName, got, tc.want)
			}
		})
	}
}

// runReadOnlyFailureLoop drives one tool-loop iteration in which a single
// builtin tool call fails, using the given name form for the incoming call
// (registry always holds the namespaced form). It returns the per-call
// display recorded on the ToolResult and the final turn output.
func runReadOnlyFailureLoop(t *testing.T, callName string) (string, string) {
	t.Helper()
	deps := ToolLoopDeps{
		MaxIterations: 1,
		BuiltinHandler: func(ctx context.Context, name string, args map[string]any) (string, error) {
			return "", errors.New("search backend rate-limited")
		},
	}
	turn := &Turn{
		User:  &config.UserConfig{Name: "tester", Role: "child"},
		Tools: []Tool{{Name: "builtin__web_search", ReadOnly: true}},
	}
	turn.Output = ""
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call1",
		Function: llm.ToolCallFunction{Name: callName, Arguments: map[string]any{"query": "sample"}},
	}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "hello"}})
	deps.ClientFactory = func(*Turn) llm.Chatter {
		return &bareNameMockChatter{responses: []llm.Message{{Content: "Sorry, the search failed."}}}
	}
	if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage tool loop error: %v", err)
	}
	if len(turn.ToolCalls) == 0 {
		t.Fatalf("no ToolResult recorded; call %q did not execute", callName)
	}
	return turn.ToolCalls[0].Output, turn.Output
}

// bareNameMockChatter returns canned responses and never emits tool calls.
// (Kept distinct from the file-local mockChatter so this file stays
// self-contained.)
type bareNameMockChatter struct {
	responses []llm.Message
	messages  []llm.Message
}

func (m *bareNameMockChatter) Chat(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	if len(m.responses) == 0 {
		return "", errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp.Content, nil
}

func (m *bareNameMockChatter) ChatMessage(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (*llm.Message, error) {
	if len(m.responses) == 0 {
		return nil, errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return &resp, nil
}

func (m *bareNameMockChatter) ChatWithTools(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int, toolDefs []llm.ToolDef) (*llm.Message, error) {
	m.messages = append([]llm.Message(nil), msgs...)
	if len(m.responses) == 0 {
		return nil, errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return &resp, nil
}

func (m *bareNameMockChatter) ChatSync(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (string, error) {
	return m.Chat(ctx, messages, temp, maxTokens, nil)
}

func (m *bareNameMockChatter) Ping(context.Context) error { return nil }

// TestStageToolLoop_ReadOnlyBareNameEndToEnd exercises the exact name form
// that mismatched the per-call turnReadOnly lookup: builtins are advertised
// to the LLM under their bare names (toolsToLLMDefs strips "builtin__"), so
// local models commonly emit the bare form ("web_search") even though the
// registry entry is namespaced ("builtin__web_search"). Both forms must
// yield the declaration-aware failure display without asserting effects.
func TestStageToolLoop_ReadOnlyBareNameEndToEnd(t *testing.T) {
	const readOnlyNote = "marked read-only"
	const perCallReadOnly = ToolErrorDisplayReadOnly

	for _, form := range []struct {
		name     string
		callName string
	}{
		{"bare form as advertised to the LLM", "web_search"},
		{"namespaced registry form", "builtin__web_search"},
	} {
		t.Run(form.name, func(t *testing.T) {
			display, output := runReadOnlyFailureLoop(t, form.callName)
			if display != perCallReadOnly {
				t.Errorf("per-call display for %q = %q, want %q", form.callName, display, perCallReadOnly)
			}
			if strings.Contains(output, "changes may") {
				t.Errorf("final output uses the mutation warning for read-only tool %q: %q", form.callName, output)
			}
			if !strings.Contains(output, readOnlyNote) {
				t.Errorf("final output does not contain %q: %q", readOnlyNote, output)
			}
		})
	}
}

// TestStageToolLoopWebSearchMarker reaches the handler and the next LLM call
// with both names a model can emit, including a marker with private details.
func TestStageToolLoopWebSearchMarker(t *testing.T) {
	for _, name := range []string{"web_search", "builtin__web_search"} {
		for _, tc := range []struct {
			name, result, want string
		}{
			{"safe", WebSearchMarkerRateLimited, WebSearchMarkerRateLimited},
			{"unsafe", WebSearchMarkerRateLimited + ": 429 from 10.0.0.7", ToolErrorDisplayReadOnly},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				var dispatched string
				client := &bareNameMockChatter{responses: []llm.Message{{Content: "Search failed."}}}
				deps := ToolLoopDeps{
					MaxIterations: 1,
					ClientFactory: func(*Turn) llm.Chatter { return client },
					BuiltinHandler: func(_ context.Context, name string, _ map[string]any) (string, error) {
						dispatched = name
						return tc.result, errors.New("private diagnostic")
					},
				}
				turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__web_search", ReadOnly: true}}}
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call1", Function: llm.ToolCallFunction{Name: name, Arguments: map[string]any{"query": "sample"}}}})
				turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "hello"}})
				if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
					t.Fatal(err)
				}
				if dispatched != "builtin__web_search" {
					t.Errorf("handler saw %q", dispatched)
				}
				var outputs []string
				for _, msg := range client.messages {
					if msg.Role == "tool" && msg.ToolCallID == "call1" {
						outputs = append(outputs, msg.Content)
					}
				}
				if len(outputs) != 1 || outputs[0] != tc.want {
					t.Errorf("LLM tool messages = %q, want [%q]", outputs, tc.want)
				}
			})
		}
	}
}

// TestPageChangeSkipAllowsBareBuiltinRetry covers a skip before name
// normalization followed by a retry in the next batch.
func TestPageChangeSkipAllowsBareBuiltinRetry(t *testing.T) {
	for _, retryName := range []string{"browser_extract", "builtin__browser_extract"} {
		t.Run(retryName, func(t *testing.T) {
			args := map[string]any{"mode": "fake-private-detail"}
			client := &bareNameMockChatter{responses: []llm.Message{
				{ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: retryName, Arguments: args}}}},
				{Content: "done"},
			}}
			var dispatched []string
			deps := ToolLoopDeps{
				ClientFactory: func(*Turn) llm.Chatter { return client },
				MaxIterations: 2, AbortQueueOnPageChange: true, RebuildHistory: true,
				BuiltinHandler: func(_ context.Context, name string, _ map[string]any) (string, error) {
					dispatched = append(dispatched, name)
					switch name {
					case "builtin__browser_navigate":
						return "URL: https://a.example/", nil
					case "builtin__browser_click":
						return "URL: https://b.example/", nil
					default:
						return "extracted", nil
					}
				},
			}
			turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{
				{Name: "builtin__browser_navigate"}, {Name: "builtin__browser_click"}, {Name: "builtin__browser_extract"},
			}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{
				{ID: "navigate", Function: llm.ToolCallFunction{Name: "builtin__browser_navigate"}},
				{ID: "click", Function: llm.ToolCallFunction{Name: "builtin__browser_click"}},
				{ID: "skip", Function: llm.ToolCallFunction{Name: "browser_extract", Arguments: args}},
			})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "extract"}})
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 4 {
				t.Fatalf("tool results = %+v", turn.ToolCalls)
			}
			skip, retry := turn.ToolCalls[2], turn.ToolCalls[3]
			if skip.ToolName != "browser_extract" || skip.Disposition != DispositionSkipped || skip.Error != nil {
				t.Errorf("unexpected skip: %+v", skip)
			}
			if retry.ToolName != "builtin__browser_extract" || retry.Disposition != DispositionAttempted || retry.Error != nil || retry.Output != "extracted" {
				t.Errorf("retry did not execute canonically: %+v", retry)
			}
			if len(dispatched) != 3 || dispatched[2] != "builtin__browser_extract" {
				t.Errorf("handler dispatches = %v", dispatched)
			}
			var history string
			for _, msg := range client.messages {
				history += msg.Content
			}
			if !strings.Contains(history, "[browser_extract args=[redacted] skip=skipped; not run]") || strings.Contains(history, "err=skipped; not run") {
				t.Errorf("rebuilt history misrepresents or leaks skip: %q", history)
			}
		})
	}
}
