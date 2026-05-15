package websearch

import "github.com/famclaw/famclaw/internal/agentcore"

func Tool(allowedRoles []string) agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__web_search",
		Source:      "builtin",
		Description: "Search the web for current information (news, weather, prices, flights, products, public docs). Returns a list of title/url/snippet results. Use this BEFORE attempting a web_fetch when you do not already know an exact URL; results carry concrete URLs and snippets you can answer from directly without a follow-up fetch in most cases.",
		Roles:       allowedRoles,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query in plain English (e.g. \"cheap flights MIA to LGA next week\"). Will be URL-encoded.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Optional cap on results returned (default 8, max 16).",
				},
			},
			"required": []string{"query"},
		},
	}
}
