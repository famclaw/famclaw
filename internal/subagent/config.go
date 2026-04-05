// Package subagent provides subagent dispatching and scheduling for FamClaw.
// Subagents are spawned by the LLM via the spawn_agent builtin tool,
// run with explicit LLM profile control, and communicate results back
// via a mailbox.
package subagent

// Config describes a subagent's execution parameters.
type Config struct {
	Prompt     string   // task description for the subagent
	LLMProfile string   // explicit LLM profile to use (parent decides)
	Tools      []string // tool allowlist (empty = inherit parent's)
	DenyTools  []string // blocklist applied after allowlist
	MaxTurns   int      // tool loop iteration limit (default 10)
	Pipeline   string   // "subagent" or "subagent_safe"
}

// Result is the output from a completed subagent.
type Result struct {
	AgentID string
	Output  string
	Error   error
}

// ApprovalRequest is sent when a subagent's tool call needs parent approval.
type ApprovalRequest struct {
	AgentID  string
	ToolName string
	Args     map[string]any
	Response chan bool // true = approved, false = denied
}
