package familystate

import (
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

// ProposeTool returns the tool definition for builtin__propose_family_fact.
// The handler lives in internal/agent because the parent-vs-child split
// requires the per-Agent OPA evaluator (auto-apply branch is gated by the
// synthetic family_fact_proposal_auto_apply rule).
func ProposeTool() agentcore.Tool {
	return agentcore.Tool{
		Name:   "builtin__propose_family_fact",
		Source: "builtin",
		Description: strings.Join([]string{
			"Propose a new family fact. Anyone can call this.",
			"",
			"WHEN PARENT: the fact is applied immediately (subject to an OPA policy check).",
			"WHEN CHILD: a proposal is sent to parents for approval; the fact is applied only after a parent approves.",
			"",
			"Example: propose_family_fact(category=\"user_preferences\", subject=\"teo\", label=\"favorite_pizza\", value=\"pepperoni\", reason=\"asked in chat\")",
		}, "\n"),
		Roles: []string{"parent", "child"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{"type": "string"},
				"subject":  map[string]any{"type": "string"},
				"label":    map[string]any{"type": "string"},
				"value":    map[string]any{"type": "string"},
				"reason":   map[string]any{"type": "string", "description": "Why this fact matters; helps the parent decide."},
			},
			"required": []string{"category", "subject", "label", "value"},
		},
	}
}

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
