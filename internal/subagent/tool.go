package subagent

import (
	"github.com/famclaw/famclaw/internal/agentcore"
)

// SpawnAgentTool returns the tool definition for the spawn_agent builtin.
// This is added to the Turn's tool list so the parent LLM can call it.
func SpawnAgentTool() agentcore.Tool {
	return agentcore.Tool{
		Name: "builtin__spawn_agent",
		Description: "Dispatch a focused research/lookup task to a subagent. The " +
			"subagent gets its own conversation, runs a tool loop (web_fetch is " +
			"available by default), and returns a single answer string. WHEN TO " +
			"USE: parallel/independent lookups (e.g. \"compare option A and B\" " +
			"→ spawn one agent per option), questions needing >3 web_fetch " +
			"calls to assemble (deep research), or tasks where you want a fresh " +
			"context window. WHEN NOT TO USE: simple factual questions, math, " +
			"things you can answer directly, or anything chat-flow (the subagent " +
			"can't ask follow-up questions). Example call: " +
			"spawn_agent({prompt: \"find three top-rated beaches near St " +
			"Petersburg FL using current visitor reviews\", tools: [\"web_fetch\"]}). " +
			"Leave tools empty to default to [web_fetch]. Subagents cannot spawn " +
			"further subagents.",
		Source: "builtin",
		Roles:  []string{"parent"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task description for the subagent to execute",
				},
				"profile": map[string]any{
					"type":        "string",
					"description": "LLM profile name to use (e.g., 'a small-context model-local'). If empty, uses the default profile.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Maximum tool loop iterations (default 10, capped at 20)",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Subagent execution timeout in seconds. Default 300s, capped at 1800s.",
				},
				"tools": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Allowlist of tool names (MCP or builtin, e.g. \"web_fetch\") the subagent may call. Empty or missing defaults to [\"web_fetch\"]. Use deny_tools to remove tools from the effective allowlist.",
				},
				"deny_tools": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Blocklist subtracted from the tools allowlist after expansion.",
				},
			},
			"required": []string{"prompt"},
		},
	}
}
