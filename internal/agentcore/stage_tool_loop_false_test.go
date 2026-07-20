package agentcore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// TestStageToolLoop_FalseCompletionNeutralization tests that when no tool succeeds
// but the LLM output claims success, the output is neutralized with a correction.
func TestStageToolLoop_FalseCompletionNeutralization(t *testing.T) {
	cases := []struct {
		name               string
		setup              func(deps *ToolLoopDeps, turn *Turn)
		wantOutputContains string // substring that should be in the final output
		wantOutputExact    string // if non-empty, the output must equal this exactly
		wantOutputNotEqual string // if non-empty, the output must not equal this
	}{
		{
			name: "genuine tool success",
			setup: func(deps *ToolLoopDeps, turn *Turn) {
				// Builtin handler that succeeds
				deps.BuiltinHandler = func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "success", nil
				}
				turn.User = &config.UserConfig{Name: "tester", Role: "child"}
				turn.Tools = []Tool{{Name: "builtin__ok"}}
				// Simulate that the first LLM response (with tool call) has been received
				turn.Output = ""
				toolCalls := []llm.ToolCall{{
					ID:       "call1",
					Function: llm.ToolCallFunction{Name: "builtin__ok", Arguments: map[string]any{}},
				}}
				turn.SetMeta("pending_tool_calls", toolCalls)
				turn.SetMeta("llm_messages", []llm.Message{
					{Role: "user", Content: "hello"}, // placeholder
				})
				// The mockChatter should return the final response when ChatWithTools is called
				var callCount int
				deps.ClientFactory = func(*Turn) llm.Chatter {
					return &mockChatter{
						responses: []llm.Message{
							{Content: "Task completed successfully!"},
						},
						callCount: &callCount,
					}
				}
			},
			wantOutputExact: "Task completed successfully!",
		},
		{
			name: "hallucinated success with no tool call",
			setup: func(deps *ToolLoopDeps, turn *Turn) {
				// Builtin handler that fails
				deps.BuiltinHandler = func(ctx context.Context, name string, args map[string]any) (string, error) {
					return "", errors.New("builtin failed")
				}
				turn.User = &config.UserConfig{Name: "tester", Role: "child"}
				turn.Tools = []Tool{{Name: "builtin__fail"}}
				// Simulate that the first LLM response (with tool call) has been received
				turn.Output = ""
				toolCalls := []llm.ToolCall{{
					ID:       "call1",
					Function: llm.ToolCallFunction{Name: "builtin__fail", Arguments: map[string]any{}},
				}}
				turn.SetMeta("pending_tool_calls", toolCalls)
				turn.SetMeta("llm_messages", []llm.Message{
					{Role: "user", Content: "hello"}, // placeholder
				})
				// The mockChatter should return the final response when ChatWithTools is called
				var callCount int
				deps.ClientFactory = func(*Turn) llm.Chatter {
					return &mockChatter{
						responses: []llm.Message{
							{Content: "Done! I saved the file."},
						},
						callCount: &callCount,
					}
				}
			},
			wantOutputNotEqual: "Done! I saved the file.",
			wantOutputContains: "(Note: no action was actually performed — the tool was not run.)",
		},
		{
			name: "purely conversational turn (no tool attempted)",
			setup: func(deps *ToolLoopDeps, turn *Turn) {
				// No tool attempted, but output contains a success phrase
				turn.User = &config.UserConfig{Name: "tester", Role: "child"}
				turn.Tools = []Tool{{Name: "builtin__ok"}} // tool available but not used
				// No pending tool calls (so turn.ToolCalls will be empty)
				turn.SetMeta("pending_tool_calls", []llm.ToolCall{})
				turn.SetMeta("llm_messages", []llm.Message{
					{Role: "user", Content: "hello"},
				})
				// The LLM response (without tool calls) is set as the initial turn.Output
				turn.Output = "I am doing well, thank you! All set."
				// We set up a mockChatter that should not be called (since no pending tool calls)
				var callCount int
				deps.ClientFactory = func(*Turn) llm.Chatter {
					return &mockChatter{
						responses: []llm.Message{
							{Content: "This should not be called"},
						},
						callCount: &callCount,
					}
				}
			},
			wantOutputExact: "I am doing well, thank you! All set.", // must be unchanged
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := ToolLoopDeps{
				MaxIterations: 1,
			}
			turn := &Turn{}
			tc.setup(&deps, turn) // Fixed: pass deps as pointer

			stage := NewStageToolLoop(deps)

			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("stage tool loop error: %v", err)
			}

			if tc.wantOutputExact != "" {
				if turn.Output != tc.wantOutputExact {
					t.Errorf("output = %q, want exact %q", turn.Output, tc.wantOutputExact)
				}
			} else {
				if tc.wantOutputContains != "" {
					if !strings.Contains(turn.Output, tc.wantOutputContains) {
						t.Errorf("output does not contain %q: %q", tc.wantOutputContains, turn.Output)
					}
				}
				if tc.wantOutputNotEqual != "" {
					if turn.Output == tc.wantOutputNotEqual {
						t.Errorf("output should not be equal to %q, but got %q", tc.wantOutputNotEqual, turn.Output)
					}
				}
			}
		})
	}
}

// mockChatter is a simple llm.Chatter that returns a sequence of responses.
type mockChatter struct {
	responses []llm.Message
	callCount *int
}

func (m *mockChatter) Chat(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	if m.callCount != nil {
		(*m.callCount)++
	}
	if len(m.responses) == 0 {
		return "", errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp.Content, nil
}

func (m *mockChatter) ChatMessage(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (*llm.Message, error) {
	if m.callCount != nil {
		(*m.callCount)++
	}
	if len(m.responses) == 0 {
		return nil, errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return &resp, nil
}

func (m *mockChatter) ChatWithTools(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int, toolDefs []llm.ToolDef) (*llm.Message, error) {
	if m.callCount != nil {
		(*m.callCount)++
	}
	if len(m.responses) == 0 {
		return nil, errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return &resp, nil
}

func (m *mockChatter) ChatSync(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (string, error) {
	if m.callCount != nil {
		(*m.callCount)++
	}
	if len(m.responses) == 0 {
		return "", errors.New("no more responses")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp.Content, nil
}

func (m *mockChatter) Ping(context.Context) error { return nil }
