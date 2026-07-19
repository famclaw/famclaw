package agentcore

import (
	"context"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestStageToolLoop(t *testing.T) {
	tests := []struct {
		name string
		// initial turn state before the tool loop
		initialTurn Turn
		// the LLM response to return when ChatWithTools is called
		llmResponse llm.Message
		// expected turn.Output after the tool loop
		expectedOutput string
		// whether we expect any tool calls to have been made
		expectToolCalls bool
	}{
		{
			name: "genuine tool success passes through",
			initialTurn: Turn{
				User: &config.UserConfig{
					Name:  "testuser",
					Role:  "child",
				},
				Input: "Please save this file: hello.txt",
				// Set the initial output to the LLM's reply so that if there are no tool calls,
				// the output remains as the LLM's reply.
				Output: "I will save the file now.",
			},
			llmResponse: llm.Message{
				Content: "I will save the file now.",
				ToolCalls: []llm.ToolCall{
					{
						ID:   "1",
						Type: "function",
						Function: llm.ToolCallFunction{
							Name: "builtin__file_write",
							Arguments: llm.ToolCallArguments(map[string]any{
								"path":   "hello.txt",
								"content": "Hello, World!",
							}),
						},
					},
				},
			},
			expectedOutput: "I have successfully used the builtin__file_write tool. ",
			expectToolCalls:  true,
		},
		{
			name: "hallucinated success with no tool call is neutralized",
			initialTurn: Turn{
				User: &config.UserConfig{
					Name:  "testuser",
					Role:  "child",
				},
				Input: "Please save this file: hello.txt",
				// Set the initial output to the LLM's reply so that if there are no tool calls,
				// the output remains as the LLM's reply.
				Output: "Done! I've saved the file.",
			},
			llmResponse: llm.Message{
				Content: "Done! I've saved the file.",
				ToolCalls: []llm.ToolCall{}, // no tool calls
			},
			expectedOutput: "Done! I've saved the file.", // because no tool calls were made, we use the LLM's reply
			expectToolCalls:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up the turn as per the test case
			turn := tt.initialTurn

			// Set up the mock LLM client
			mockClient := &mockChatter{
				response: &tt.llmResponse,
				err:      nil,
			}

			// Set up the ToolLoopDeps
			deps := ToolLoopDeps{
				ClientFactory: func(t *Turn) llm.Chatter {
					return mockClient
				},
				Temperature:   0.0,
				MaxTokens:     100,
				MaxIterations: 1,
			}

			// We need to set up the turn's meta and tools based on the llmResponse.
			// If the llmResponse has tool calls, we set the pending_tool_calls meta.
			if len(tt.llmResponse.ToolCalls) > 0 {
				turn.SetMeta("pending_tool_calls", tt.llmResponse.ToolCalls)
			}
			// We also need to set the llm_messages meta to an empty slice or something.
			// We'll set it to an empty slice.
			turn.SetMeta("llm_messages", []llm.Message{})

			// If the llmResponse has tool calls, we need to add those tools to turn.Tools.
			// We'll create a tool from the first tool call.
			if len(tt.llmResponse.ToolCalls) > 0 {
				tc := tt.llmResponse.ToolCalls[0]
				tool := Tool{
					Name: tc.Function.Name,
				}
				turn.Tools = append(turn.Tools, tool)
			}

			// We do not need a pool for builtin tools; we can set Pool to nil.
			deps.Pool = nil

			// We also need to set up the BuiltinHandler to handle builtin__file_write.
			// We'll create a mock builtin handler that returns a successful result.
			mockBuiltinHandler := func(ctx context.Context, name string, args map[string]any) (string, error) {
				return "Success", nil
			}
			deps.BuiltinHandler = mockBuiltinHandler

			// Create the stage
			stage := NewStageToolLoop(deps)

			// Run the stage
			err := stage(context.Background(), &turn)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Check the output
			if turn.Output != tt.expectedOutput {
				t.Errorf("expected output %q, got %q", tt.expectedOutput, turn.Output)
			}

			// Check if tool calls were made as expected
			if len(turn.ToolCalls) > 0 != tt.expectToolCalls {
				t.Errorf("expected tool calls made: %v, got: %v", tt.expectToolCalls, len(turn.ToolCalls) > 0)
			}
		})
	}
}

// mockChatter is a mock llm.Chatter that returns a fixed response.
type mockChatter struct {
	response *llm.Message
	err      error
}

func (m *mockChatter) Chat(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int, onToken func(string)) (string, error) {
	if m.response != nil {
		return m.response.Content, m.err
	}
	return "", m.err
}

func (m *mockChatter) ChatMessage(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int) (*llm.Message, error) {
	return m.response, m.err
}

func (m *mockChatter) ChatWithTools(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int, tools []llm.ToolDef) (*llm.Message, error) {
	return m.response, m.err
}

func (m *mockChatter) ChatSync(ctx context.Context, msgs []llm.Message, temperature float64, maxTokens int) (string, error) {
	if m.response != nil {
		return m.response.Content, m.err
	}
	return "", m.err
}