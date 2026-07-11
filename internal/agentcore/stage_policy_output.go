package agentcore

import (
	"context"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/policy"
)

const outputGateBlockedMessage = "I'm unable to send this response right now. Please try again."

// NewStagePolicyOutput returns a stage that evaluates LLM output against the
// OPA output policy. Fail-closed: on evaluation error or denial the turn
// output is replaced with a user-friendly safe message and the underlying
// reason is logged to stderr for operator audit. No error is returned so the
// pipeline does not abort on a policy eval hiccup.
func NewStagePolicyOutput(eval *policy.Evaluator) Stage {
	return func(ctx context.Context, turn *Turn) error {
		if eval == nil {
			log.Printf("[stage_policy_output] nil evaluator — fail-closed")
			turn.Output = outputGateBlockedMessage
			return nil
		}
		userRole := turn.User.Role
		ageGroup := turn.User.AgeGroup
		gateway := "" // Turn does not carry a gateway field

		dec, err := eval.EvaluateOutput(ctx, policy.OutputInput{
			User:          policy.UserInput{Role: userRole, AgeGroup: ageGroup},
			Gateway:       gateway,
			DraftResponse: turn.Output,
		})
		if err != nil {
			// Fail-closed: block on policy evaluation error
			log.Printf("[stage_policy_output] EvaluateOutput error (fail-closed): %v", err)
			turn.Output = outputGateBlockedMessage
			return nil
		}
		if !dec.Allow {
			// dec.Reason is an internal audit string ("hard-blocked content
			// detected", "role not recognized", etc.). Don't leak it to the
			// user — keep it in logs and emit a friendly fallback.
			log.Printf("[stage_policy_output] output blocked: %s", dec.Reason)
			turn.Output = outputGateBlockedMessage
			turn.SetMeta("output_blocked", true)
			return nil
		}
		if len(dec.Redact) > 0 {
			out := turn.Output
			for _, s := range dec.Redact {
				if s == "" {
					continue
				}
				out = strings.ReplaceAll(out, s, "[redacted]")
			}
			turn.Output = out
		}
		return nil
	}
}
