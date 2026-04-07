package agentcore

import "time"

// Tool describes a callable tool available to the LLM.
type Tool struct {
	Name        string         // namespaced: "mcp__weather__forecast" or "builtin__spawn_agent"
	Description string         // one-line description for LLM context
	InputSchema map[string]any // JSON Schema for parameters
	Source      string         // "mcp", "builtin", "plugin"
	ServerName  string         // which MCP server owns this tool (empty for builtins)
	ScanTarget  string         // URL or path for security scanning (empty = skip)
	Roles       []string       // allowed roles (empty = all roles)
}

// ToolResult captures one tool call and its outcome.
type ToolResult struct {
	ToolName string
	Args     map[string]any
	Output   string
	Error    error
	Duration time.Duration
}

// AllowedForRole returns true if the tool is available for the given role.
// An empty Roles list means the tool is available to all roles.
func (t *Tool) AllowedForRole(role string) bool {
	if len(t.Roles) == 0 {
		return true
	}
	for _, r := range t.Roles {
		if r == role {
			return true
		}
	}
	return false
}
