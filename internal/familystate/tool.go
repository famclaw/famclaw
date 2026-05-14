package familystate

import (
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

// GetTool returns the tool definition for builtin__get_family_state. The
// handler lives in internal/agent (agent.handleGetFamilyState) because it
// needs access to the per-Agent config (for known-subject filtering) and
// the *Store. Read tool — open to every role.
func GetTool() agentcore.Tool {
	return agentcore.Tool{
		Name:   "builtin__get_family_state",
		Source: "builtin",
		Description: strings.Join([]string{
			"Read the family's stored facts: pets, important_dates, allergies, dietary_restrictions, and any custom categories the parents have added.",
			"",
			"WHEN TO USE: the user asks something where family-specific knowledge matters — pet name, family member's birthday, what foods are kept in the house, who has what allergy.",
			"WHEN NOT TO USE: generic questions with no family-specific component. Pure factual lookups (weather, news) belong in web_fetch.",
			"",
			"Example call: get_family_state(category=\"pets\") to list pets, or get_family_state() with no category to see every category in one response.",
		}, "\n"),
		Roles: []string{"parent", "child"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"description": "Optional category filter — e.g. 'pets', 'important_dates', 'allergies'. Omit to read every category.",
				},
			},
		},
	}
}
