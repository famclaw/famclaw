package subagent

import (
	"github.com/famclaw/famclaw/internal/agentcore"
)

// SpawnAgentTool returns the tool definition for the spawn_agent builtin.
// This is added to the Turn's tool list so the parent LLM can call it.
func SpawnAgentTool() agentcore.Tool {
	return agentcore.Tool{
		Name: "builtin__spawn_agent",
		Description: "Dispatch a task to a subagent running on a different LLM. " +
			"The subagent runs the task with access to MCP tools and returns " +
			"the result. Use this to delegate compute-intensive work, research, " +
			"or tasks that benefit from a specialized model.",
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
					"description": "LLM profile name to use (e.g., 'qwen3-local'). If empty, uses the default profile.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"description": "Maximum tool loop iterations (default 10)",
				},
			},
			"required": []string{"prompt"},
		},
	}
}
