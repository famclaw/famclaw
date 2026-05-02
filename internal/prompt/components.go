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
