package agentcore

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/policy"
)

// ErrToolBlocked is returned when a tool call is blocked by policy.
var ErrToolBlocked = fmt.Errorf("tool call blocked by policy")

// ErrApprovalRequested is returned when a tool call requires parent approval
// and a pending approval has been created for it. The tool was NOT executed.
var ErrApprovalRequested = fmt.Errorf("tool call pending parent approval")

// ErrApprovalPending is returned when a tool call's approval is still
// pending (already created in a prior turn). The tool was NOT executed.
var ErrApprovalPending = fmt.Errorf("tool call approval still pending")

// ToolPolicyEvaluator checks whether a tool call is allowed for the current
// user. The OPA implementation lives in internal/policy; tests can supply
// a fake.
type ToolPolicyEvaluator interface {
	EvaluateToolCall(ctx context.Context, input policy.ToolCallInput) (policy.ToolDecision, error)
}

// NewStagePolicyToolCall returns a stage that checks each tool call on the
// turn against the OPA tool_policy rules. Blocked calls have ToolResult.Error
// set to ErrToolBlocked so the tool loop reports the failure to the LLM.
// Calls requiring approval have ToolResult.Error set to ErrApprovalRequested
// (this stage cannot create approvals — that happens in the tool loop).
//
// The bare tool name (with the "builtin__" / "mcp__server__" prefix stripped)
// is passed to OPA so the same Rego rule applies whether the tool is a
// builtin or an MCP-served one.
func NewStagePolicyToolCall(eval ToolPolicyEvaluator) Stage {
	return func(ctx context.Context, turn *Turn) error {
		if eval == nil || len(turn.ToolCalls) == 0 {
			return nil
		}
		for i := range turn.ToolCalls {
			tc := &turn.ToolCalls[i]
			if tc.Error != nil {
				continue
			}
			input := policy.ToolCallInput{
				User: policy.UserInput{
					Role:     turn.User.Role,
					AgeGroup: turn.User.AgeGroup,
					Name:     turn.User.Name,
				},
				ToolName: bareToolName(tc.ToolName),
			}
			decision, err := eval.EvaluateToolCall(ctx, input)
			if err != nil {
				log.Printf("[stage_policy_tool] eval error for %s: %v (treating as block)", tc.ToolName, err)
				tc.Error = ErrToolBlocked
				continue
			}
			if !decision.Allow {
				if decision.Action == "request_approval" {
					tc.Error = ErrApprovalRequested
				} else {
					tc.Error = ErrToolBlocked
				}
			}
		}
		return nil
	}
}

// bareToolName strips the "builtin__" or "mcp__<server>__" prefix from a
// namespaced tool name so OPA rules can match on the unqualified name.
func bareToolName(name string) string {
	if rest, ok := strings.CutPrefix(name, "builtin__"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		// mcp__<server>__<tool> — drop the server segment.
		if i := strings.Index(rest, "__"); i >= 0 {
			return rest[i+2:]
		}
		return rest
	}
	return name
}
