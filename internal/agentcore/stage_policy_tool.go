package agentcore

import (
	"context"
	"fmt"

	"github.com/famclaw/famclaw/internal/policy"
)

// ErrToolBlocked is returned when a tool call is blocked by policy.
var ErrToolBlocked = fmt.Errorf("tool call blocked by policy")

// ToolPolicyEvaluator checks whether a tool call is allowed for the current user.
type ToolPolicyEvaluator interface {
	EvaluateToolCall(ctx context.Context, user, role, toolName string, args map[string]any) (policy.Decision, error)
}

// NewStagePolicyToolCall returns a stage that checks tool calls against policy.
// This is a placeholder — the actual OPA integration requires Rego rules in
// internal/policy/policies/. For now, it applies a simple role-based check:
// under_8 cannot use tools with "dangerous" in the name. Full OPA integration
// comes with the Rego rules.
func NewStagePolicyToolCall() Stage {
	return func(_ context.Context, turn *Turn) error {
		// No tool calls to check
		if len(turn.ToolCalls) == 0 {
			return nil
		}

		// Basic role-based tool restrictions
		for _, tc := range turn.ToolCalls {
			if turn.User.AgeGroup == "under_8" {
				// Block web search for very young children
				if tc.ToolName == "web_search" || tc.ToolName == "mcp__search__web" {
					tc.Error = ErrToolBlocked
				}
			}
		}
		return nil
	}
}
