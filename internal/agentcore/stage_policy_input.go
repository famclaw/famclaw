package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// ErrPolicyBlock is returned when policy blocks the message.
// The caller should check turn.Policy for the decision details.
var ErrPolicyBlock = fmt.Errorf("policy blocked")

// NewStagePolicyInput returns a stage that evaluates input policy via OPA.
// If the policy decision is not "allow", it sets turn.Output and returns ErrPolicyBlock.
func NewStagePolicyInput(evaluator *policy.Evaluator, db *store.DB) Stage {
	return func(ctx context.Context, turn *Turn) error {
		requestID := approvalID(turn.User.Name, string(turn.Category))
		approvals, _ := db.AllApprovalsForOPA()

		decision, err := evaluator.Evaluate(ctx, policy.Input{
			User: policy.UserInput{
				Role:     turn.User.Role,
				AgeGroup: turn.User.AgeGroup,
				Name:     turn.User.Name,
			},
			Query:     policy.QueryInput{Category: string(turn.Category), Text: turn.Input},
			RequestID: requestID,
			Approvals: approvals,
		})
		if err != nil {
			decision = policy.Decision{Action: "block", Reason: "Policy evaluation error"}
		}

		turn.Policy = decision

		if decision.Action != "allow" {
			turn.Output = policyMessage(decision)
			return ErrPolicyBlock
		}
		return nil
	}
}

func policyMessage(d policy.Decision) string {
	switch d.Action {
	case "block":
		return fmt.Sprintf("I'm sorry, I can't help with that topic. %s", d.Reason)
	case "request_approval":
		return "I've asked a parent to approve this topic for you. They'll get a notification — once they approve, just ask me again!"
	case "pending":
		return "A parent has already been notified about this request. Once they approve, you can ask me!"
	default:
		return "I'm unable to answer that right now."
	}
}

func approvalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}
