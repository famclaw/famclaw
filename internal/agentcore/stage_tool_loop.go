package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
)

// ToolLoopDeps holds dependencies for the tool loop stage.
type ToolLoopDeps struct {
	Pool          *mcp.Pool
	ClientFactory func(turn *Turn) *llm.Client
	Temperature   float64
	MaxTokens     int
	MaxIterations int // default 10
	// BuiltinHandler dispatches builtin tools (spawn_agent, etc.) that are
	// not in the MCP pool. Keyed by tool name prefix "builtin__".
	BuiltinHandler func(ctx context.Context, name string, args map[string]any) (string, error)
}

// NewStageToolLoop returns a stage that executes MCP tool calls from LLM responses.
func NewStageToolLoop(deps ToolLoopDeps) Stage {
	if deps.MaxIterations == 0 {
		deps.MaxIterations = 10
	}

	return func(ctx context.Context, turn *Turn) error {
		if deps.Pool == nil && deps.BuiltinHandler == nil {
			return nil
		}

		// Check if LLM call produced tool calls
		pendingRaw, ok := turn.GetMeta("pending_tool_calls")
		if !ok {
			return nil
		}
		pendingCalls, ok := pendingRaw.([]llm.ToolCall)
		if !ok || len(pendingCalls) == 0 {
			return nil
		}

		msgsRaw, _ := turn.GetMeta("llm_messages")
		llmMsgs, _ := msgsRaw.([]llm.Message)
		if llmMsgs == nil {
			return nil
		}

		client := deps.ClientFactory(turn)
		if client == nil {
			return nil
		}

		// Build the assistant message with tool calls
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   turn.Output,
			ToolCalls: pendingCalls,
		}
		llmMsgs = append(llmMsgs, assistantMsg)

		// Execute tool calls
		for i := 0; i < deps.MaxIterations; i++ {
			if len(pendingCalls) == 0 {
				break
			}

			// Build allowlist from turn.Tools — only tools offered to the LLM
			// can be called, even if the pool or handler would accept them.
			turnAllowed := make(map[string]bool, len(turn.Tools))
			for _, t := range turn.Tools {
				turnAllowed[t.Name] = true
			}

			for _, tc := range pendingCalls {
				log.Printf("[agentcore][%s] tool_call: %s", turn.User.Name, tc.Function.Name)
				start := time.Now()

				// Reject tools not in the turn's allowlist (prevents
				// hallucinated/injected calls from bypassing role filtering).
				if len(turnAllowed) > 0 && !turnAllowed[tc.Function.Name] {
					llmMsgs = append(llmMsgs, llm.Message{
						Role:    "tool",
						Content: fmt.Sprintf("Error: tool %q not available", tc.Function.Name),
					})
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName: tc.Function.Name,
						Args:     tc.Function.Arguments,
						Error:    fmt.Errorf("tool %q not in turn allowlist", tc.Function.Name),
						Duration: time.Since(start),
					})
					continue
				}

				// Builtin tools (spawn_agent, etc.) route to the handler, not MCP pool
				if strings.HasPrefix(tc.Function.Name, "builtin__") && deps.BuiltinHandler != nil {
					result, err := deps.BuiltinHandler(ctx, tc.Function.Name, tc.Function.Arguments)
					duration := time.Since(start)
					if err != nil {
						llmMsgs = append(llmMsgs, llm.Message{
							Role:    "tool",
							Content: fmt.Sprintf("Error: %v", err),
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName: tc.Function.Name,
							Args:     tc.Function.Arguments,
							Error:    err,
							Duration: duration,
						})
					} else {
						llmMsgs = append(llmMsgs, llm.Message{
							Role:    "tool",
							Content: result,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName: tc.Function.Name,
							Args:     tc.Function.Arguments,
							Output:   result,
							Duration: duration,
						})
					}
					continue
				}

				if deps.Pool == nil || !deps.Pool.HasTool(tc.Function.Name) {
					llmMsgs = append(llmMsgs, llm.Message{
						Role:    "tool",
						Content: fmt.Sprintf("Error: unknown tool %q", tc.Function.Name),
					})
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName: tc.Function.Name,
						Args:     tc.Function.Arguments,
						Error:    fmt.Errorf("unknown tool %q", tc.Function.Name),
						Duration: time.Since(start),
					})
					continue
				}

				result, err := deps.Pool.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
				duration := time.Since(start)

				var toolText string
				if err != nil {
					toolText = fmt.Sprintf("Error calling %s: %v", tc.Function.Name, err)
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName: tc.Function.Name,
						Args:     tc.Function.Arguments,
						Error:    err,
						Duration: duration,
					})
				} else {
					if result != nil && len(result.Content) > 0 {
						resultJSON, _ := json.Marshal(result.Content)
						toolText = string(resultJSON)
					}
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName: tc.Function.Name,
						Args:     tc.Function.Arguments,
						Output:   toolText,
						Duration: duration,
					})
				}

				llmMsgs = append(llmMsgs, llm.Message{
					Role:    "tool",
					Content: toolText,
				})
			}

			// Call LLM again with tool results
			msg, err := client.ChatMessage(ctx, llmMsgs, deps.Temperature, deps.MaxTokens)
			if err != nil {
				return fmt.Errorf("LLM error in tool loop iteration %d: %w", i+1, err)
			}

			turn.Output = msg.Content
			pendingCalls = msg.ToolCalls

			if len(pendingCalls) > 0 {
				llmMsgs = append(llmMsgs, *msg)
			}
		}

		return nil
	}
}
