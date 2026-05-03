package webfetch

import "github.com/famclaw/famclaw/internal/agentcore"

// Tool returns the tool definition for the web_fetch builtin. The handler
// lives in internal/agent (agent.handleWebFetch) so it has access to per-user
// config and the URL allowlist.
func Tool(allowedRoles []string) agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__web_fetch",
		Source:      "builtin",
		Description: "Fetch a URL and return extracted text. Use for current events, weather, or public documentation. Subject to family policy and per-user URL allowlist; size and timeout are capped.",
		Roles:       allowedRoles,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Absolute http(s) URL to fetch",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Optional response size cap in bytes (default 262144).",
				},
			},
			"required": []string{"url"},
		},
	}
}
