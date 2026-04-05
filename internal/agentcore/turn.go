// Package agentcore implements the composable pipeline engine for FamClaw's agent.
// Each user message flows through a sequence of stages (classify, policy, LLM, etc.)
// assembled into pipelines for different modes (family chat, CLI, subagent).
package agentcore

import (
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/policy"
)

// Turn holds all state for one user message through the pipeline.
// Each stage reads and writes fields on the Turn as it processes.
type Turn struct {
	// Input
	User  *config.UserConfig
	Input string // raw user message

	// Built by stages
	Messages []Message           // conversation history being built
	Category classifier.Category // set by StageClassify
	Policy   policy.Decision     // set by StagePolicyInput

	// Tools
	Tools     []Tool       // available tools after filtering
	ToolCalls []ToolResult // tool calls made this turn

	// Output
	Output     string // final response text
	LLMProfile string // which LLM profile to use
	Streamed   bool   // whether the response was streamed

	// Metadata lets stages pass arbitrary data forward.
	Metadata map[string]any
}

// Message is a conversation message (mirrors llm.Message to avoid circular import).
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the tool name and arguments.
type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// SetMeta stores a value in the turn's metadata map (initializes if nil).
func (t *Turn) SetMeta(key string, value any) {
	if t.Metadata == nil {
		t.Metadata = make(map[string]any)
	}
	t.Metadata[key] = value
}

// GetMeta retrieves a value from the turn's metadata map.
func (t *Turn) GetMeta(key string) (any, bool) {
	if t.Metadata == nil {
		return nil, false
	}
	v, ok := t.Metadata[key]
	return v, ok
}
