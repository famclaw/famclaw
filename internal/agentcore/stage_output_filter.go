package agentcore

import (
	"context"
	"strings"
)

// outputBlockedPatterns are keywords that should never appear in LLM responses to children.
var outputBlockedPatterns = []string{
	"suicide", "kill yourself", "self-harm", "cutting yourself",
	"pornography", "sexual intercourse", "explicit content",
	"racial slur", "ethnic cleansing", "white supremac",
	"how to make a bomb", "how to steal", "how to hack",
}

// NewStageOutputFilter returns a stage that checks the LLM output for blocked patterns.
// Only applies to non-parent users. Will be replaced by OPA policy_output in a later PR.
func NewStageOutputFilter() Stage {
	return func(_ context.Context, turn *Turn) error {
		if turn.User.Role == "parent" {
			return nil
		}
		lower := strings.ToLower(turn.Output)
		for _, pattern := range outputBlockedPatterns {
			if strings.Contains(lower, pattern) {
				turn.Output = "I generated a response that might not be appropriate. Let me try a different approach — could you rephrase your question?"
				break
			}
		}
		return nil
	}
}
