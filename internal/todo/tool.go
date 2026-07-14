package todo

import "github.com/famclaw/famclaw/internal/agentcore"

// Tool returns the tool definition for the builtin__todo builtin.
// allowedRoles: if nil or empty, all roles can use the tool.
func Tool(allowedRoles []string) agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__todo",
		Source:      "builtin",
		Description: "Manage your personal todo list. Actions: add, list, complete, remove. Todos are scoped to your user account.",
		Roles:       allowedRoles,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Action to perform: add, list, complete, remove",
					"enum":        []string{"add", "list", "complete", "remove"},
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Todo text (required for 'add' action)",
				},
				"id": map[string]any{
					"type":        "integer",
					"description": "Todo ID (required for 'complete' and 'remove' actions)",
				},
				"filter": map[string]any{
					"type":        "string",
					"description": "Filter for 'list' action: all, active (default), completed",
					"enum":        []string{"all", "active", "completed"},
				},
			},
			"required": []string{"action"},
		},
	}
}