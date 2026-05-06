package agentcore

import (
	"context"
	"strings"

	"github.com/famclaw/famclaw/internal/policy"
)

// NewStagePolicyOutput returns a stage that evaluates LLM output against the
// OPA output policy. Fail-closed: on evaluation error the turn output is
// replaced with a safe message and no error is returned (so the pipeline does
// not abort on a policy eval hiccup).
func NewStagePolicyOutput(eval *policy.Evaluator) Stage {
	return func(ctx context.Context, turn *Turn) error {
		if eval == nil {
			turn.Output = "I'm unable to send this response right now. Please try again."
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
			turn.Output = "I'm unable to send this response right now. Please try again."
			return nil
		}
		if !dec.Allow {
			turn.Output = dec.Reason
			return nil
		}
		if len(dec.Redact) > 0 {
			out := turn.Output
			for _, s := range dec.Redact {
				out = strings.ReplaceAll(out, s, "[redacted]")
			}
			turn.Output = out
		}
		return nil
	}
}
