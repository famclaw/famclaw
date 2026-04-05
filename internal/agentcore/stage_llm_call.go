package agentcore

import (
	"context"
	"fmt"

	"github.com/famclaw/famclaw/internal/llm"
)

// LLMCallDeps holds dependencies for the LLM call stage.
type LLMCallDeps struct {
	ClientFactory func(turn *Turn) *llm.Client // creates a client for this turn's LLM profile
	Temperature   float64
	MaxTokens     int
	OnToken       func(string) // streaming callback (can be nil)
}

// NewStageLLMCall returns a stage that calls the LLM with the turn's messages.
// If tools are available, uses non-streaming ChatWithTools.
// Otherwise, uses streaming Chat.
func NewStageLLMCall(deps LLMCallDeps) Stage {
	return func(ctx context.Context, turn *Turn) error {
		client := deps.ClientFactory(turn)
		if client == nil {
			return fmt.Errorf("LLM not configured — open the web UI to set up your AI backend")
		}

		// Convert turn messages to llm.Message
		llmMsgs := turnToLLMMessages(turn.Messages)

		if len(turn.Tools) > 0 {
			// Non-streaming with tools
			toolDefs := toolsToLLMDefs(turn.Tools)
			msg, err := client.ChatWithTools(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, toolDefs)
			if err != nil {
				return fmt.Errorf("LLM error: %w", err)
			}
			turn.Output = msg.Content
			// Store tool calls for the tool loop stage
			if len(msg.ToolCalls) > 0 {
				turn.SetMeta("pending_tool_calls", msg.ToolCalls)
				turn.SetMeta("llm_messages", llmMsgs)
			}
			return nil
		}

		// Streaming without tools
		response, err := client.Chat(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, deps.OnToken)
		if err != nil {
			return fmt.Errorf("LLM error: %w", err)
		}
		turn.Output = response
		turn.Streamed = deps.OnToken != nil
		return nil
	}
}

func turnToLLMMessages(msgs []Message) []llm.Message {
	result := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		result[i] = llm.Message{
			Role:    m.Role,
			Content: m.Content,
		}
		if len(m.ToolCalls) > 0 {
			result[i].ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				result[i].ToolCalls[j] = llm.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: llm.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
	}
	return result
}

func toolsToLLMDefs(tools []Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = llm.ToolDef{
			Type: "function",
			Function: llm.ToolDefFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return defs
}
