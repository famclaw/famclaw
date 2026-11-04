package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// ApprovalStore provides approval persistence for the tool-call approval
// flow. *store.DB satisfies this interface.
type ApprovalStore interface {
	UpsertApproval(a *store.Approval) (bool, error)
	AllApprovalsForOPA() (map[string]any, error)
}

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

// toolApprovalID computes a deterministic, day-scoped request ID for a
// child's tool call. Two calls with the same user, tool, and args on the
// same UTC day produce the same ID, so a re-issued call finds the existing
// pending approval instead of creating a duplicate.
func toolApprovalID(userName, toolName string, args map[string]any) string {
	day := time.Now().UTC().Format("2006-01-02")
	argJSON, _ := json.Marshal(args)
	h := sha256.Sum256([]byte(userName + ":" + toolName + ":" + string(argJSON) + ":" + day))
	return hex.EncodeToString(h[:8])
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
	// ApprovalStore provides approval persistence for the tool-call
	// approval flow. When nil, request_approval decisions fail closed
	// (the tool does not execute).
	ApprovalStore ApprovalStore
	// RebuildHistory toggles the model-authored history rebuild path.
	// When true, each LLM call inside the tool loop is sent
	// [system, rebuilt_user_message] where rebuilt_user_message is
	// produced by RebuildUserMessage from the accumulated HistoryItem
	// slice. When false (default), the legacy append-tool-result path
	// runs. This flag will become the default in a follow-up phase
	// after Phase 1c populates eval/memory/next_goal from model output.
	RebuildHistory bool
	// AbortQueueOnPageChange aborts remaining tool calls in a single LLM
	// response when an earlier mutating call changed the page state (URL or
	// active element). Without this guard, queued actions execute against the
	// stale ref table from before the change. Default false.
	AbortQueueOnPageChange bool
	// EnableLoopDetector enables the per-turn ActionLoopDetector. The detector
	// surfaces escalating prose nudges via the rebuilt user_message when the
	// agent repeats the same action 5/8/12 times in a 20-action window. Has no
	// effect unless RebuildHistory is also true (the nudge is injected into the
	// rebuilt user message). Default false.
	EnableLoopDetector bool
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

		// Per-turn loop detector — tracks action hashes in a rolling window
		// and surfaces prose nudges when the same action repeats.
		var detector *ActionLoopDetector
		if deps.EnableLoopDetector {
			detector = NewActionLoopDetector(0)
		}

		// prevURL tracks the most recent URL seen in any mutating tool result.
		// Used by AbortQueueOnPageChange across all iterations of the turn.
		var prevURL string

		// Build the assistant message with tool calls
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   turn.Output,
			ToolCalls: pendingCalls,
		}
		llmMsgs = append(llmMsgs, assistantMsg)

		// Capture system prompt and user request for rebuild mode before
		// the loop mutates llmMsgs.
		var history []HistoryItem
		var rbSystemPrompt, rbUserRequest string
		if deps.RebuildHistory {
			for _, m := range llmMsgs {
				if rbSystemPrompt == "" && m.Role == "system" {
					rbSystemPrompt = m.Content
				}
				if m.Role == "user" {
					rbUserRequest = m.Content
				}
			}
		}

		// lastModelText is the text portion of the most recent LLM response —
		// emitted alongside the tool_calls now in pendingCalls. Initialized
		// from turn.Output (the pre-loop assistant response); updated at the
		// end of each iteration after ChatWithTools returns.
		lastModelText := turn.Output

		// Execute tool calls
		for i := 0; i < deps.MaxIterations; i++ {
			if len(pendingCalls) == 0 {
				break
			}

			// Record the turn.ToolCalls length before this iteration so we
			// can collect only this iteration's results for the history item.
			tcStart := len(turn.ToolCalls)

			// Build allowlist from turn.Tools — only tools offered to the LLM
			// can be called, even if the pool or handler would accept them.
			turnAllowed := make(map[string]bool, len(turn.Tools))
			for _, t := range turn.Tools {
				turnAllowed[t.Name] = true
			}

			// pageChanged is reset per-batch: set when a mutating tool call
			// produces a different URL than the last known one.
			pageChanged := false

			// batchImages is reset per-batch and holds base64 data URIs from
			// MCP image content (android-control screenshots). It never
			// reaches llmMsgs: the describe step below consumes it and only
			// text continues.
			var batchImages []string

			for _, tc := range pendingCalls {
				// Abort remaining queued calls if an earlier mutating action
				// changed the page — those refs are now stale.
				if pageChanged && deps.AbortQueueOnPageChange {
					const skipMsg = "[skipped: page changed earlier in batch — re-evaluate]"
					llmMsgs = append(llmMsgs, llm.Message{
						Role:       "tool",
						Content:    skipMsg,
						ToolCallID: tc.ID,
					})
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Output:      skipMsg,
						Disposition: DispositionSkipped,
						Duration:    0,
					})
					continue
				}
				log.Printf("[agentcore] tool_call")
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

				// Suppress identical unsuccessful or uncertain calls within this
				// turn. An uncertain outcome must not be described as a failure:
				// the original operation may already have taken effect.
				if prior, found := priorUnsuccessfulCall(tc.Function.Name, tc.Function.Arguments, turn.ToolCalls); found {
					reason := "this exact tool call already failed earlier in this turn; try a different approach instead of repeating it"
					if prior.Disposition == DispositionAttempted && prior.Outcome == OutcomeUnknown {
						reason = "the outcome of this exact tool call is uncertain; verify before trying again"
					}
					injectToolError(&llmMsgs, turn, tc, ErrToolAlreadyFailed, reason, start)
					continue
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
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Error:       fmt.Errorf("tool %q not in turn allowlist", tc.Function.Name),
						Disposition: DispositionDenied,
						Duration:    time.Since(start),
					})
					continue
				}

				// OPA tool_policy gate — replaces the older hardcoded keyword
				// block. On evaluator error, fail closed and log the cause
				// internally; never leak raw evaluator error text to the LLM
				// transcript (could expose internal paths / module names).
				//
				// Action semantics (matching decision.rego):
				//   "allow"              → proceed to execution
				//   "request_approval"   → create pending approval, tell the
				//                          model the parent is being asked
				//   "pending"            → parent already notified, wait
				//   "block"              → refuse with a reason
				if deps.PolicyEvaluator != nil {
					requestID := toolApprovalID(turn.User.Name, bareToolName(tc.Function.Name), tc.Function.Arguments)
					input := toolPolicyInput(turn, tc.Function.Name)
					input.Args = tc.Function.Arguments
					input.RequestID = requestID
					if deps.ApprovalStore != nil {
						var apErr error
						input.Approvals, apErr = deps.ApprovalStore.AllApprovalsForOPA()
						if apErr != nil {
							// Fail closed: if we cannot read existing approvals,
							// the policy cannot see a prior denial. Proceeding
							// would let a denied executable file_write look like
							// a fresh request — failing open on a child-safety
							// boundary. Block instead.
							log.Printf("[stage_tool_loop] approval store read failed for %s: %v (failing closed)", tc.Function.Name, apErr)
							injectToolError(&llmMsgs, turn, tc, ErrToolBlocked, "approval unavailable", start)
							continue
						}
					}

					decision, perr := deps.PolicyEvaluator.EvaluateToolCall(ctx, input)
					if perr != nil {
						log.Printf("[stage_tool_loop] policy evaluator error for %s: %v (failing closed)", tc.Function.Name, perr)
						injectToolError(&llmMsgs, turn, tc, ErrToolBlocked, "blocked by policy", start)
						continue
					}

					switch decision.Action {
					case "allow":
						// proceed to execution below
					case "request_approval":
						// Create a pending approval scoped to this child +
						// tool call. If the store is unavailable or the
						// insert fails, fail CLOSED — the tool must not run.
						if deps.ApprovalStore == nil {
							log.Printf("[stage_tool_loop] approval store nil for %s (failing closed)", tc.Function.Name)
							injectToolError(&llmMsgs, turn, tc, ErrToolBlocked, "approval unavailable", start)
							continue
						}
						a := &store.Approval{
							ID:          requestID,
							UserName:    turn.User.Name,
							UserDisplay: turn.User.DisplayName,
							AgeGroup:    turn.User.AgeGroup,
							Category:    "tool:" + bareToolName(tc.Function.Name),
							QueryText:   approvalDescription(bareToolName(tc.Function.Name), tc.Function.Arguments),
						}
						if _, err := deps.ApprovalStore.UpsertApproval(a); err != nil {
							log.Printf("[stage_tool_loop] approval creation failed for %s: %v (failing closed)", tc.Function.Name, err)
							injectToolError(&llmMsgs, turn, tc, ErrToolBlocked, "approval unavailable", start)
							continue
						}
						// Tell the LLM the parent is being asked. The tool
						// did NOT execute.
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    "I'm asking a parent for permission to do this. They'll decide soon.",
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName:    tc.Function.Name,
							Args:        tc.Function.Arguments,
							Error:       ErrApprovalRequested,
							Disposition: DispositionPending,
							Duration:    time.Since(start),
						})
						continue
					case "pending":
						// A parent was already notified in a prior turn; the
						// approval is still waiting. Don't re-request.
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    "A parent has been notified about this request. I'll try again once they decide.",
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName:    tc.Function.Name,
							Args:        tc.Function.Arguments,
							Error:       ErrApprovalPending,
							Disposition: DispositionPending,
							Duration:    time.Since(start),
						})
						continue
					default:
						// "block" or anything unrecognized — fail closed.
						reason := decision.Reason
						if reason == "" {
							reason = "blocked by policy"
						}
						injectToolError(&llmMsgs, turn, tc, ErrToolBlocked, reason, start)
						continue
					}
				}

				// Builtin tools (spawn_agent, etc.) route to the handler, not MCP pool
				if strings.HasPrefix(tc.Function.Name, "builtin__") && deps.BuiltinHandler != nil {
					result, err := deps.BuiltinHandler(ctx, tc.Function.Name, tc.Function.Arguments)
					duration := time.Since(start)
					if err != nil {
						log.Printf("[agentcore] builtin tool reported an error (%s)", duration)
						display := "Tool reported an error; some changes may still have occurred. Verify before trying again."
						if tc.Function.Name == "builtin__web_search" && safeSearchMarker(result) {
							display = result
						}
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    display,
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName:    tc.Function.Name,
							Args:        tc.Function.Arguments,
							Error:       err,
							Output:      display,
							Outcome:     OutcomeFailure,
							Disposition: DispositionAttempted,
							Duration:    duration,
						})
					} else {
						log.Printf("[agentcore][%s] tool_ok: %s bytes=%d (%s)", turn.User.Name, tc.Function.Name, len(result), duration)
						llmMsgs = append(llmMsgs, llm.Message{
							Role:       "tool",
							Content:    result,
							ToolCallID: tc.ID,
						})
						turn.ToolCalls = append(turn.ToolCalls, ToolResult{
							ToolName:    tc.Function.Name,
							Args:        tc.Function.Arguments,
							Output:      result,
							Outcome:     OutcomeSuccess,
							Disposition: DispositionAttempted,
							Duration:    duration,
						})
						if deps.AbortQueueOnPageChange && isMutatingBrowserAction(tc.Function.Name) {
							if u := extractFirstURL(result); u != "" {
								if prevURL != "" && u != prevURL {
									pageChanged = true
								}
								prevURL = u
							}
						}
					}
					if detector != nil {
						detector.Push(tc.Function.Name, tc.Function.Arguments)
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
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Error:       fmt.Errorf("unknown tool %q", tc.Function.Name),
						Disposition: DispositionSkipped,
						Duration:    time.Since(start),
					})
					continue
				}

				result, err := deps.Pool.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
				duration := time.Since(start)

				var toolText string
				if err != nil {
					toolText = "Tool outcome uncertain; verify before trying again."
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Error:       err,
						Outcome:     OutcomeUnknown,
						Disposition: DispositionAttempted,
						Duration:    duration,
					})
				} else if result == nil || result.IsError {
					outcome := OutcomeFailure
					if result == nil {
						outcome = OutcomeUnknown
					}
					toolText = "Tool reported an error; some changes may still have occurred. Verify before trying again."
					if result == nil {
						toolText = "Tool outcome uncertain; verify before trying again."
					}
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Error:       fmt.Errorf("tool did not return a successful result"),
						Outcome:     outcome,
						Disposition: DispositionAttempted,
						Duration:    duration,
					})
				} else {
					var images []string
					if result != nil && len(result.Content) > 0 {
						toolText, images = splitMCPContent(result.Content)
					}
					if len(images) > 0 {
						// MCP image results (android-control screenshots)
						// must never be JSON-stringified into the transcript
						// (the old behaviour), nor appended to llmMsgs as raw
						// image_url parts: image+tools in one request makes
						// local multimodal models silently drop tool calls
						// (data/fc-vision-plus-tools), base64 blows the
						// context window, and a tool-role image is invisible
						// to the vision path. Instead the raw base64 stays in
						// this loop-local buffer: after the batch we run the
						// no-tools describe step once (describeBatchImages)
						// and only its text description continues into the
						// follow-up call, history rebuild, compression and
						// any audit copy. The base64 reaches exactly one
						// place: the describe request.
						batchImages = append(batchImages, images...)
						note := fmt.Sprintf("[screenshot returned by %s; vision description follows]", tc.Function.Name)
						if toolText != "" {
							note += ": " + toolText
						}
						toolText = note
					}
					turn.ToolCalls = append(turn.ToolCalls, ToolResult{
						ToolName:    tc.Function.Name,
						Args:        tc.Function.Arguments,
						Output:      toolText,
						Outcome:     OutcomeSuccess,
						Disposition: DispositionAttempted,
						Duration:    duration,
					})
				}

				llmMsgs = append(llmMsgs, llm.Message{
					Role:       "tool",
					Content:    toolText,
					ToolCallID: tc.ID,
				})
				if detector != nil {
					detector.Push(tc.Function.Name, tc.Function.Arguments)
				}
				if err == nil && deps.AbortQueueOnPageChange && isMutatingBrowserAction(tc.Function.Name) {
					if u := extractFirstURL(toolText); u != "" {
						if prevURL != "" && u != prevURL {
							pageChanged = true
						}
						prevURL = u
					}
				}
			}

			// Vision describe: MCP image tool results (android-control
			// screenshots) are consumed here — one no-tools describe call for
			// the whole batch — so only a text description continues into
			// the follow-up LLM call. Never send image parts alongside tool
			// definitions in this loop (see data/fc-vision-plus-tools).
			var visionNote string
			if len(batchImages) > 0 {
				visionNote = describeBatchImages(ctx, client, deps.Temperature, batchImages)
			}

			if deps.RebuildHistory {
				// Collect tool results from this iteration and append a history item.
				iterResults := turn.ToolCalls[tcStart:]
				resultStrs := make([]string, 0, len(iterResults))
				for _, tr := range iterResults {
					resultStrs = append(resultStrs, rebuildResultLine(tr))
				}
				if visionNote != "" {
					resultStrs = append(resultStrs, visionNote)
				}
				eval, memory, nextGoal := ParseSelfSummary(lastModelText)
				if eval == "" {
					eval = "(n/a)"
				}
				if memory == "" {
					memory = "(n/a)"
				}
				if nextGoal == "" {
					nextGoal = "(n/a)"
				}
				history = append(history, HistoryItem{
					StepNum:  i + 1,
					Eval:     eval,
					Memory:   memory,
					NextGoal: nextGoal,
					Results:  resultStrs,
				})
				// Rebuild the full message slice from accumulated history so
				// the next LLM call sees [system, fresh_user_message_block]
				// instead of the ever-growing append of raw tool messages.
				// Only prepend a system message if the original conversation
				// had one — forcing an empty system message changes the
				// prompt shape and can alter model behavior.
				stepInfo := ""
				if detector != nil {
					if nudge := detector.Nudge(); nudge != "" {
						stepInfo = nudge
					}
				}
				llmMsgs = llmMsgs[:0]
				if rbSystemPrompt != "" {
					llmMsgs = append(llmMsgs, llm.Message{Role: "system", Content: rbSystemPrompt})
				}
				llmMsgs = append(llmMsgs, llm.Message{
					Role:    "user",
					Content: RebuildUserMessage(history, AgentState{UserRequest: rbUserRequest, StepInfo: stepInfo}, nil),
				})
			} else {
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
			}

			// Legacy path: hand the vision description to the follow-up call
			// as a user-role message (text only — the image parts were
			// consumed by the describe step and never enter this buffer).
			if visionNote != "" && !deps.RebuildHistory {
				llmMsgs = append(llmMsgs, llm.Message{Role: "user", Content: visionNote})
			}

			// Call LLM again with tool results
			toolDefs := toolsToLLMDefs(turn.Tools)
			msg, err := client.ChatWithTools(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, toolDefs)
			if err != nil {
				return fmt.Errorf("LLM error in tool loop iteration %d: %w", i+1, err)
			}

			turn.Output = msg.Content
			pendingCalls = msg.ToolCalls
			lastModelText = msg.Content

			if !deps.RebuildHistory && len(pendingCalls) > 0 {
				llmMsgs = append(llmMsgs, *msg)
			}
		}
		// Summarize attempted outcomes separately from policy and skipped
		// dispositions, including turns that exhaust the iteration budget.
		warning := outcomeWarning(turn.ToolCalls, turn.Tools)
		if len(pendingCalls) > 0 {
			// The model's last text is an intermediate report, not a completed
			// answer: it still requests calls the budget prevented us from
			// executing. Preserve that text and trail it with the disclosure
			// notes instead of replacing the whole output.
			capNote := "Tool iteration limit reached; remaining requested calls were not run."
			if warning != "" {
				capNote = warning + " " + capNote
			}
			if turn.Output != "" {
				turn.Output = turn.Output + "\n\n" + capNote
			} else {
				turn.Output = capNote
			}
		} else if warning != "" {
			if turn.Output == "" {
				turn.Output = warning
			} else {
				turn.Output = warning + "\n\n" + turn.Output
			}
		}

		return nil
	}
}

// outcomeWarning makes no claim that a failure had no effects, and never
// treats an unexecuted policy or duplicate disposition as an execution.
// The exception is tools declared ReadOnly in the registry: a read-only
// call cannot mutate external state, so its failure or uncertainty is
// reported without the "changes may have occurred" warning. Tools absent
// from the registry stay conservatively treated as potentially mutating.
func outcomeWarning(calls []ToolResult, registry []Tool) string {
	readOnly := make(map[string]bool, len(registry)*2)
	for _, tool := range registry {
		if tool.ReadOnly {
			readOnly[tool.Name] = true
			readOnly[bareToolName(tool.Name)] = true
		}
	}
	isReadOnly := func(name string) bool {
		return readOnly[name] || readOnly[bareToolName(name)]
	}

	var success bool
	var mutationFailure, readOnlyFailure, mutationUnknown, readOnlyUnknown bool
	var denied, pending, skipped bool
	for _, call := range calls {
		switch call.Disposition {
		case DispositionAttempted:
			switch call.Outcome {
			case OutcomeSuccess:
				success = true
			case OutcomeFailure:
				if isReadOnly(call.ToolName) {
					readOnlyFailure = true
				} else {
					mutationFailure = true
				}
			default:
				if isReadOnly(call.ToolName) {
					readOnlyUnknown = true
				} else {
					mutationUnknown = true
				}
			}
		case DispositionDenied:
			denied = true
		case DispositionPending:
			pending = true
		case DispositionSkipped:
			skipped = true
		}
	}
	var notes []string
	if mutationFailure && success {
		notes = append(notes, "Some tool calls succeeded, but another reported a failure; some changes may have occurred. Verify before trying again.")
	} else if mutationFailure {
		notes = append(notes, "The tool reported a failure; some changes may still have occurred. Verify before trying again.")
	} else if readOnlyFailure && success {
		notes = append(notes, "Some tool calls succeeded, but a read-only tool failed. No changes were made by the failed call.")
	} else if readOnlyFailure {
		notes = append(notes, "The tool reported a failure. No changes were made.")
	} else if success && (mutationUnknown || readOnlyUnknown) {
		notes = append(notes, "Some tool calls succeeded.")
	}
	if mutationUnknown {
		notes = append(notes, "A tool outcome is uncertain; some changes may have occurred. Verify before trying again.")
	} else if readOnlyUnknown {
		notes = append(notes, "A tool outcome is uncertain; no changes were made.")
	}
	if denied {
		notes = append(notes, "A tool call was denied and was not run.")
	}
	if pending {
		notes = append(notes, "A tool call is pending approval and was not run.")
	}
	if skipped {
		notes = append(notes, "A tool call was skipped and was not run.")
	}
	return strings.Join(notes, " ")
}

// injectToolError appends a tool-error message to llmMsgs and records a
// failed ToolResult on the turn. Extracts the repeated error-injection
// pattern used across the policy gate and tool-dispatch paths.
func injectToolError(llmMsgs *[]llm.Message, turn *Turn, tc llm.ToolCall, err error, reason string, start time.Time) {
	disposition := DispositionDenied
	if err == ErrToolAlreadyFailed {
		disposition = DispositionSkipped
	}
	*llmMsgs = append(*llmMsgs, llm.Message{
		Role:       "tool",
		Content:    fmt.Sprintf("Error: %s", reason),
		ToolCallID: tc.ID,
	})
	turn.ToolCalls = append(turn.ToolCalls, ToolResult{
		ToolName:    tc.Function.Name,
		Args:        tc.Function.Arguments,
		Error:       err,
		Disposition: disposition,
		Duration:    time.Since(start),
	})
}

// ErrToolAlreadyFailed is set on a ToolResult when the tool loop detects
// that an identical (tool name + arguments) call already failed earlier in
// this turn. The call is short-circuited -- not re-executed -- to break the
// repeated apology-retry loop where the model re-emits a call that already
// failed. It complements the policy gates ErrToolBlocked /
// ErrApprovalRequested / ErrApprovalPending, which gate execution via the
// OPA tool_policy engine.
var ErrToolAlreadyFailed = fmt.Errorf("tool call already failed this turn")

// priorUnsuccessfulCall finds a matching prior error or attempted non-success.
// Map comparison is order independent; successful attempts may run again.
// The scan is bounded by the current turn's iteration and batch limits.
func priorUnsuccessfulCall(name string, args map[string]any, results []ToolResult) (ToolResult, bool) {
	for _, r := range results {
		if r.Error == nil && !(r.Disposition == DispositionAttempted && r.Outcome != OutcomeSuccess) {
			continue
		}
		if r.ToolName == name && reflect.DeepEqual(r.Args, args) {
			return r, true
		}
	}
	return ToolResult{}, false
}

// approvalDescription builds a human-readable summary of the tool call
// that a parent sees when reviewing a pending approval. It is derived from
// the actual tool name and arguments - never hardcoded - so the parent
// knows exactly what they are approving.
func approvalDescription(toolName string, args map[string]any) string {
	var b strings.Builder
	b.WriteString(toolName)
	b.WriteString(" - ")
	if path, ok := args["path"].(string); ok && path != "" {
		b.WriteString("path ")
		b.WriteString(path)
	} else {
		b.WriteString("no path specified")
	}
	return b.String()
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

// isMutatingBrowserAction reports whether name is a browser action that can
// change the current page (navigate, click, fill, select, press_key). These
// are the actions checked for URL changes by the AbortQueueOnPageChange guard.
func isMutatingBrowserAction(name string) bool {
	switch name {
	case "builtin__browser_navigate",
		"builtin__browser_click",
		"builtin__browser_fill",
		"builtin__browser_select",
		"builtin__browser_press_key":
		return true
	}
	return false
}

// safeSearchMarker accepts only fixed, non-diagnostic search classifications.
func safeSearchMarker(s string) bool {
	switch s {
	case "web_search: host not permitted", "web_search: provider not configured",
		"web_search: rate limited", "web_search: search backend error",
		"web_search: search timed out", "web_search: search backend unreachable",
		"web_search: search backend auth or config error", "web_search: invalid search backend response",
		"web_search: unavailable":
		return true
	default:
		return false
	}
}

// extractFirstURL parses the URL from a tool result that starts with
// "URL: <url>\n". Returns "" when the text does not start with that prefix.
// This mirrors the snapshotReply format used by the browser handlers.
func extractFirstURL(text string) string {
	const prefix = "URL: "
	if !strings.HasPrefix(text, prefix) {
		return ""
	}
	rest := text[len(prefix):]
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

// rebuildResultLine formats one ToolResult into the bracketed history string
// used by the RebuildHistory path. Format:
//
//	[toolname args=<json,120char> ok bytes=N]
//	[toolname args=<json,120char> err=message]
//
// The whole line is truncated to 240 characters.
func rebuildResultLine(tr ToolResult) string {
	argsJSON := "{}"
	if len(tr.Args) > 0 {
		if b, err := json.Marshal(tr.Args); err == nil {
			argsJSON = string(b)
		}
	}
	const argsMax = 120
	if len(argsJSON) > argsMax {
		argsJSON = argsJSON[:argsMax]
	}

	var line string
	if tr.Error != nil || tr.Disposition == DispositionSkipped {
		// Diagnostic causes and input arguments can contain secrets. Render
		// only trusted structural state and allowlisted search classifications.
		state := "tool reported an error"
		switch tr.Disposition {
		case DispositionDenied:
			state = "denied; not run"
		case DispositionPending:
			state = "approval pending; not run"
		case DispositionSkipped:
			state = "skipped; not run"
		case DispositionAttempted:
			if tr.Outcome == OutcomeUnknown {
				state = "outcome uncertain; effects may have occurred"
			} else if tr.ToolName == "builtin__web_search" && safeSearchMarker(tr.Output) {
				state = tr.Output
			}
		}
		line = fmt.Sprintf("[%s args=[redacted] err=%s]", tr.ToolName, state)
	} else {
		line = fmt.Sprintf("[%s args=%s ok bytes=%d]", tr.ToolName, argsJSON, len(tr.Output))
	}
	const lineMax = 240
	if len(line) > lineMax {
		line = line[:lineMax]
	}
	return line
}

// sanitizeHistoryText collapses CR, LF, and tab characters into single
// spaces so a value embedded in a one-line history result cannot break
// the line-per-result contract of the rebuilt prompt block.
func sanitizeHistoryText(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
}

// shouldPrependFailureNote reports whether the false-completion correction
// note should be prepended to the model's output.
//
// The decision is purely structural — it never inspects the output text —
// so it is language-agnostic by construction: a false completion is
// detected when the model produced a final answer (modelStopped) but
// every tool call in the turn failed. Because the check has no language
// to match, a success claim in any language (English, Spanish, Japanese,
// Arabic, etc.) is caught exactly when the tools actually failed, and
// genuine completions (where at least one tool succeeded) are never
// flagged.
//
// The caller owns the note wording and the prepend/append decision; this
// function only answers whether this turn is a false completion.
func shouldPrependFailureNote(toolCalls []ToolResult, modelStopped bool) bool {
	if !modelStopped || len(toolCalls) == 0 {
		return false
	}
	for _, tr := range toolCalls {
		if tr.Error == nil {
			return false
		}
	}
	return true
}
