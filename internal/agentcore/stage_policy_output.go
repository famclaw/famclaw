package agentcore

import (
	"context"
	"strings"
)

// NewStagePolicyOutput returns a stage that evaluates LLM output against policy.
// Currently uses the same pattern-based approach as StageOutputFilter, but
// structured as a proper policy stage that can be migrated to OPA rules.
// When OPA output rules exist, this stage calls the evaluator instead.
func NewStagePolicyOutput() Stage {
	return func(_ context.Context, turn *Turn) error {
		if turn.User.Role == "parent" {
			return nil
		}

		lower := strings.ToLower(turn.Output)

		// Check critical-risk patterns that should never reach children
		for _, pattern := range criticalPatterns {
			if strings.Contains(lower, pattern) {
				turn.Output = "I generated a response that might not be appropriate. Let me try a different approach — could you rephrase your question?"
				turn.SetMeta("output_filtered", true)
				turn.SetMeta("output_filter_reason", "critical_pattern: "+pattern)
				return nil
			}
		}

		// Age-specific filtering
		switch turn.User.AgeGroup {
		case "under_8":
			for _, pattern := range under8Patterns {
				if strings.Contains(lower, pattern) {
					turn.Output = "That's a topic for grown-ups! Let's talk about something fun instead."
					turn.SetMeta("output_filtered", true)
					return nil
				}
			}
		case "age_8_12":
			for _, pattern := range age8to12Patterns {
				if strings.Contains(lower, pattern) {
					turn.Output = "I'd like to give you a better answer on that topic. Could you ask a parent to help?"
					turn.SetMeta("output_filtered", true)
					return nil
				}
			}
		}

		return nil
	}
}

// criticalPatterns are blocked for ALL non-parent users.
var criticalPatterns = []string{
	"suicide", "kill yourself", "self-harm", "cutting yourself",
	"pornography", "sexual intercourse", "explicit content",
	"racial slur", "ethnic cleansing", "white supremac",
	"how to make a bomb", "how to steal", "how to hack",
}

// under8Patterns are additionally blocked for very young children.
var under8Patterns = []string{
	"violence", "blood", "murder", "weapon",
	"death", "dying", "dead body",
	"drug", "alcohol", "cigarette",
}

// age8to12Patterns are additionally blocked for 8-12 year olds.
var age8to12Patterns = []string{
	"detailed violence", "graphic injury",
	"drug use", "alcohol abuse",
}
