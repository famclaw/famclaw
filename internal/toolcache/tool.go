package toolcache

import "github.com/famclaw/famclaw/internal/agentcore"

// Tool returns the tool definition for builtin__tool_result_more.
// The handler lives in internal/agent because it needs access to per-turn
// user identity and the Cache instance held by the Agent — it's a thin
// dispatch into Cache.More.
//
// Roles: pass the same allowed_roles set used for web_fetch. Cross-user
// access is enforced at the cache layer regardless of policy decisions
// (Cache.More returns ErrNotFound when user_name doesn't match the row),
// so a permissive role gate here can't bypass per-user ownership.
func Tool(allowedRoles []string) agentcore.Tool {
	return agentcore.Tool{
		Name:   "builtin__tool_result_more",
		Source: "builtin",
		Description: "Read more bytes from a previously-spilled tool result. " +
			"Use when a prior tool reply (typically web_fetch) was truncated " +
			"and you need additional content. Provide the cache id from the " +
			"truncation marker.",
		Roles: allowedRoles,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Cache id from a truncation marker emitted by a prior tool call",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "0-indexed byte offset to start reading from (default 0)",
				},
				"length": map[string]any{
					"type":        "integer",
					"description": "How many bytes to read; capped at 8192 (default 4096)",
				},
			},
			"required": []string{"id"},
		},
	}
}
