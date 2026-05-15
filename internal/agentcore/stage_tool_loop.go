package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
)

// toolPolicyInput shapes a per-call OPA input from the turn user.
func toolPolicyInput(turn *Turn, toolName string) policy.ToolCallInput {
	return policy.ToolCallInput{
		User: policy.UserInput{
			Role:     turn.User.Role,
			AgeGroup: turn.User.AgeGroup,
			Name:     turn.User.Name,
		},
		ToolName: bareToolName(toolName),
	}
}

// ToolLoopDeps holds dependencies for the tool loop stage.
type ToolLoopDeps struct {
	Pool          *mcp.Pool
	ClientFactory func(turn *Turn) llm.Chatter
	Temperature   float64
	MaxTokens     int
	MaxIterations int // default 10
	// ContextWindow, when > 0, enables per-iteration compression of the
	// llmMsgs buffer before each follow-up LLM call. Tool reply messages
	// are marked Prunable so they get evicted before user/assistant turns
	// when over budget. This is the v0.5.9 web_fetch overflow fix.
	ContextWindow int
	// BuiltinHandler dispatches builtin tools (spawn_agent, etc.) that are
	// not in the MCP pool. Keyed by tool name prefix "builtin__".
	BuiltinHandler func(ctx context.Context, name string, args map[string]any) (string, error)
	// PolicyEvaluator gates each tool call through the OPA tool_policy rules.
	// When nil, no per-call policy enforcement is applied (useful for tests).
	PolicyEvaluator ToolPolicyEvaluator
}

// NewStageToolLoop returns a stage that executes MCP tool calls from LLM responses.
func NewStageToolLoop(deps ToolLoopDeps) Stage {
	if deps.MaxIterations == 0 {
		deps.MaxIterations = 25
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
				log.Printf("[agentcore][%s] tool_call: %s args=%s", turn.User.Name, tc.Function.Name, summarizeArgs(tc.Function.Arguments))
				start := time.Now()

				// Local LLMs occasionally emit the bare tool name
				// ("web_fetch") instead of the namespaced form
				// ("builtin__web_fetch"). If the bare name is unqualified
				// and the prefixed builtin form is in the turn allowlist,
				// rewrite to the canonical form for dispatch. Mismatches
				// still fall through to the unknown-tool error below.
				if !strings.HasPrefix(tc.Function.Name, "builtin__") && !strings.HasPrefix(tc.Function.Name, "mcp__") {
					if turnAllowed["builtin__"+tc.Function.Name] {
						log.Printf("[agentcore][%s] tool_call normalized: %s -> builtin__%s",
							turn.User.Name, tc.Function.Name, tc.Function.Name)
						tc.Function.Name = "builtin__" + tc.Function.Name
					}
				}

				// Reject tools not in the turn's allowlist (prevents
				// hallucinated/injected calls from bypassing role filtering).
				if len(turnAllowed) > 0 && !turnAllowed[tc.Function.Name] {
					llmMsgs = append(llmMsgs, llm.Message{
						Role:       "tool",
						Content:    fmt.Sprintf("Error: tool %q not available", tc.Function.Name),
						ToolCallID: tc.ID,
					})
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName: tc.Function.Name,
						Args:     tc.Function.Arguments,
						Error:    fmt.Errorf("tool %q not in turn allowlist", tc.Function.Name),
						Duration: time.Since(start),
					})
					continue
				}

				// OPA tool_policy gate — replaces the older hardcoded keyword
				// block. On evaluator error, fail closed and log the cause
				// internally; never leak raw evaluator error text to the LLM
				// transcript (could expose internal paths / module names).
				if deps.PolicyEvaluator != nil {
					decision, perr := deps.PolicyEvaluator.EvaluateToolCall(ctx, toolPolicyInput(turn, tc.Function.Name))
					if perr != nil || !decision.Allow {
						reason := "blocked by policy"
						if perr != nil {
							log.Printf("[stage_tool_loop] policy evaluator error for %s: %v (failing closed)", tc.Function.Name, perr)
						} else if decision.Reason != "" {
							reason = decision.Reason
						}
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    fmt.Sprintf("Error: %s", reason),
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName: tc.Function.Name,
							Args:     tc.Function.Arguments,
							Error:    ErrToolBlocked,
							Duration: time.Since(start),
						})
						continue
					}
				}

				// Builtin tools (spawn_agent, etc.) route to the handler, not MCP pool
				if strings.HasPrefix(tc.Function.Name, "builtin__") && deps.BuiltinHandler != nil {
					result, err := deps.BuiltinHandler(ctx, tc.Function.Name, tc.Function.Arguments)
					duration := time.Since(start)
					if err != nil {
						log.Printf("[agentcore][%s] tool_err: %s err=%v (%s)", turn.User.Name, tc.Function.Name, err, duration)
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    fmt.Sprintf("Error: %v", err),
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName: tc.Function.Name,
							Args:     tc.Function.Arguments,
							Error:    err,
							Duration: duration,
						})
					} else {
						log.Printf("[agentcore][%s] tool_ok: %s bytes=%d (%s)", turn.User.Name, tc.Function.Name, len(result), duration)
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    result,
							ToolCallID: tc.ID,
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
						Role:       "tool",
						Content:    fmt.Sprintf("Error: unknown tool %q", tc.Function.Name),
						ToolCallID: tc.ID,
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
					Role:       "tool",
					Content:    toolText,
					ToolCallID: tc.ID,
				})
			}

			// Per-iteration compression. Tool messages are marked Prunable
			// so they get evicted before user/assistant turns when over
			// budget. Before this guard, a single big tool result (the
			// v0.5.9 web_fetch overflow bug) would push the next LLM call
			// past n_ctx with no recovery path.
			if deps.ContextWindow > 0 {
				prunable := make(map[int]bool, len(llmMsgs))
				for j, m := range llmMsgs {
					if m.Role == "tool" {
						prunable[j] = true
					}
				}
				compressed := compress.Compress(
					llmToCompress(llmMsgs, prunable),
					compress.Options{ContextWindow: deps.ContextWindow},
				)
				llmMsgs = compressToLLM(compressed, llmMsgs)
			}

			// Call LLM again with tool results
			toolDefs := toolsToLLMDefs(turn.Tools)
			msg, err := client.ChatWithTools(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, toolDefs)
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

// summarizeArgs renders tool-call arguments as a single-line JSON string for
// the tool_call log line. Long payloads are truncated to keep one log entry
// per call. A nil/empty map renders as "{}".
func summarizeArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	const max = 240
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
