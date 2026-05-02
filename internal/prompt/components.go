package prompt

import (
	"fmt"
	"strings"
)

// userComponent describes who the user is.
func userComponent(c BuildContext) (string, bool) {
	if c.User == nil {
		return "", false
	}
	name := c.User.DisplayName
	if name == "" {
		name = c.User.Name
	}
	role := c.User.Role
	if role == "" {
		role = "child"
	}
	return fmt.Sprintf("You are talking with %s. Their role in this family is %s.", name, role), true
}

// familyComponent lists other family members so the assistant can
// answer "what's my brother's name" etc. without leaking PII to the LLM
// vendor beyond what is already in the family config.
func familyComponent(c BuildContext) (string, bool) {
	if c.Cfg == nil || c.User == nil || len(c.Cfg.Users) <= 1 {
		return "", false
	}
	var sib []string
	for _, u := range c.Cfg.Users {
		if u.Name == c.User.Name {
			continue
		}
		label := u.DisplayName
		if label == "" {
			label = u.Name
		}
		sib = append(sib, fmt.Sprintf("%s (%s)", label, u.Role))
	}
	if len(sib) == 0 {
		return "", false
	}
	return "Other family members: " + strings.Join(sib, ", ") + ".", true
}

// ageComponent gives age-appropriate tone guidance. Replaces the
// existing ageContextPrompt helper in agent.go.
func ageComponent(c BuildContext) (string, bool) {
	if c.User == nil {
		return "", false
	}
	name := c.User.DisplayName
	if name == "" {
		name = c.User.Name
	}
	switch c.User.AgeGroup {
	case "under_8":
		return fmt.Sprintf("%s is under 8. Use very simple words and short sentences. Be playful and encouraging. Never raise scary or complex topics.", name), true
	case "age_8_12":
		return fmt.Sprintf("%s is between 8 and 12. Be friendly and educational. Explain things clearly without being condescending.", name), true
	case "age_13_17":
		return fmt.Sprintf("%s is a teenager. Be respectful and treat them as a capable young adult. You can engage with more complex topics but stay age-appropriate.", name), true
	default:
		return "", false
	}
}

// policyComponent enumerates hard-blocked categories that policy will
// reject before the LLM ever sees the message. Stating them up front
// prevents the model from generating a refusal in voice it can't sustain.
func policyComponent(c BuildContext) (string, bool) {
	if len(c.HardBlocked) == 0 {
		return "", false
	}
	return "The following topics cannot be discussed in this family — policy " +
		"will reject any message about them before reaching you: " +
		strings.Join(c.HardBlocked, ", ") + ". " +
		"If a user asks about one, briefly say it's not allowed in this family " +
		"and offer to help with something else.", true
}

// approvalsComponent explains the parent-approval flow to non-parents.
func approvalsComponent(c BuildContext) (string, bool) {
	if c.User == nil || c.User.Role == "parent" {
		return "", false
	}
	return "Some topics need parent approval before you can discuss them. If " +
		"policy returns 'request_approval', the user will see a notification " +
		"to ask their parent. You don't need to mention this proactively — " +
		"just answer normally and let policy do its job.", true
}

// capabilitiesComponent states what the assistant can actually do.
// Always included so the model never says "I can't execute code" when
// it has tools available.
func capabilitiesComponent(c BuildContext) (string, bool) {
	parts := []string{
		"You can hold conversations, answer factual questions, do math, " +
			"explain concepts, and help with homework.",
	}
	if len(c.Skills) > 0 {
		parts = append(parts,
			"You also have these skills available as tools: "+strings.Join(c.Skills, ", ")+
				". Call them when relevant.")
	}
	return strings.Join(parts, " "), true
}
