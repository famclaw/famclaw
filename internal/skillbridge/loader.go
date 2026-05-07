package skillbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SkillPromptEvaluator is implemented by *policy.Evaluator.
// Using an interface keeps skillbridge free of a direct dependency on internal/policy.
type SkillPromptEvaluator interface {
	EvaluateSkillPrompt(ctx context.Context, input SkillPromptCheckInput) (SkillPromptCheckResult, error)
}

// SkillPromptCheckInput mirrors policy.SkillPromptInput without importing internal/policy.
type SkillPromptCheckInput struct {
	SkillName  string
	PromptBody string
	UserRole   string
}

// SkillPromptCheckResult mirrors policy.SkillPromptDecision without importing internal/policy.
type SkillPromptCheckResult struct {
	Allow  bool
	Reason string
}

// LoadForPromptChecked is like LoadForPrompt but evaluates each skill's prompt body
// against the skill-prompt policy before including it. Skills that fail the policy
// check are skipped (not included in the output). Returns an error only if the
// evaluator itself fails (not on policy deny).
func LoadForPromptChecked(ctx context.Context, skills []*Skill, eval SkillPromptEvaluator, userRole string) (string, error) {
	var approved []*Skill
	for _, s := range skills {
		result, err := eval.EvaluateSkillPrompt(ctx, SkillPromptCheckInput{
			SkillName:  s.Name,
			PromptBody: s.Body,
			UserRole:   userRole,
		})
		if err != nil {
			return "", fmt.Errorf("evaluating skill %q prompt: %w", s.Name, err)
		}
		if !result.Allow {
			fmt.Fprintf(os.Stderr, "skillbridge: skill %q blocked by policy: %s\n", s.Name, result.Reason)
			continue
		}
		approved = append(approved, s)
	}
	return LoadForPrompt(approved), nil
}

// LoadForPrompt formats skills into AgentSkills XML for system prompt injection.
// This format is compatible with OpenClaw and PicoClaw.
func LoadForPrompt(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<AgentSkills>\n")
	for _, s := range skills {
		sb.WriteString(formatSkillXML(s))
	}
	sb.WriteString("</AgentSkills>")
	return sb.String()
}

func formatSkillXML(s *Skill) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  <Skill name=%q", s.Name))
	if s.Description != "" {
		sb.WriteString(fmt.Sprintf(" description=%q", s.Description))
	}
	if s.Version != "" {
		sb.WriteString(fmt.Sprintf(" version=%q", s.Version))
	}
	sb.WriteString(">\n")
	// Body injected verbatim — no modification
	for _, line := range strings.Split(s.Body, "\n") {
		sb.WriteString("    " + line + "\n")
	}
	sb.WriteString("  </Skill>\n")
	return sb.String()
}
