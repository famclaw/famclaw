package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/famclaw/famclaw/internal/policy"
)

// EvaluateAndApply runs the OPA output policy against a draft response.
// Applies redactions (strings.ReplaceAll with "[redacted]") when the policy
// allows with partial redactions. Returns (draft, true, nil) unchanged when
// fully allowed with no redactions. Returns ("", false, nil) when blocked.
// Returns an error only on policy evaluation failure.
func EvaluateAndApply(
	ctx context.Context,
	eval *policy.Evaluator,
	draft string,
	user policy.UserInput,
	gateway string,
) (final string, allowed bool, err error) {
	dec, err := eval.EvaluateOutput(ctx, policy.OutputInput{
		User:          user,
		Gateway:       gateway,
		DraftResponse: draft,
	})
	if err != nil {
		return "", false, fmt.Errorf("output policy evaluation: %w", err)
	}
	if !dec.Allow {
		return "", false, nil
	}
	if len(dec.Redact) > 0 {
		out := draft
		for _, s := range dec.Redact {
			out = strings.ReplaceAll(out, s, "[redacted]")
		}
		return out, true, nil
	}
	return draft, true, nil
}
