package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
)

// ExecutorDeps holds the dependencies needed to run a subagent.
type ExecutorDeps struct {
	Pool        *mcp.Pool      // MCP tool pool (shared with parent)
	Config      *config.Config // full config (for profile resolution)
	Temperature float64
	MaxTokens   int

	// BuiltinDefs are the OpenAI-style tool definitions for parent
	// builtins (web_fetch, spawn_agent, admin tools) that should be
	// exposed to the subagent. Without these the subagent only has
	// MCP tools — most deployments have no MCP servers configured,
	// so the subagent would have ZERO tools and just regurgitate
	// training data, defeating the point of spawn_agent.
	BuiltinDefs []llm.ToolDef
	// BuiltinHandler dispatches a builtin tool call by namespaced name
	// (e.g. "builtin__web_fetch") and returns the tool's stringified
	// result. Mirrors the parent agent's makeBuiltinHandler shape so the
	// subagent shares the parent's tool implementations and policy
	// posture (the parent already passed the OPA tool gate to even reach
	// here).
	BuiltinHandler func(ctx context.Context, name string, args map[string]any) (string, error)
}

// filterBuiltinDefs applies the same allowlist/denylist as filterTools
// but to builtin tool defs. Caller's allow entries may be in bare form
// ("web_fetch") or namespaced ("builtin__web_fetch") — both match, so a
// model that emits either name shape gets the same outcome. Returns the
// subset of defs allowed AND a name-set for call-time enforcement
// against the namespaced name (defense in depth: blocks hallucinated
// builtin names even if the LLM tries one not in the def list).
func filterBuiltinDefs(defs []llm.ToolDef, allow, deny []string) ([]llm.ToolDef, map[string]bool) {
	if len(defs) == 0 || len(allow) == 0 {
		return nil, map[string]bool{}
	}
	allowSet := make(map[string]bool, len(allow)*2)
	for _, a := range allow {
		allowSet[a] = true
		allowSet["builtin__"+a] = true
	}
	denySet := make(map[string]bool, len(deny)*2)
	for _, d := range deny {
		denySet[d] = true
		denySet["builtin__"+d] = true
	}
	var out []llm.ToolDef
	allowed := make(map[string]bool)
	for _, def := range defs {
		name := def.Function.Name
		if !allowSet[name] {
			continue
		}
		if denySet[name] {
			continue
		}
		allowed[name] = true
		out = append(out, def)
	}
	return out, allowed
}

// filterTools applies the default-deny allowlist + denylist to MCP tool infos.
// Empty allow yields zero tools and an empty allowedTools map (so the
// subagent has NO MCP tools by default). Denied entries are subtracted after
// the allowlist filter.
func filterTools(infos []mcp.ToolInfo, allow, deny []string) ([]llm.ToolDef, map[string]bool) {
	allowed := make(map[string]bool)
	if len(allow) == 0 {
		return nil, allowed
	}
	allowSet := make(map[string]bool, len(allow))
	for _, a := range allow {
		allowSet[a] = true
	}
	denied := make(map[string]bool, len(deny))
	for _, d := range deny {
		denied[d] = true
	}
	var tools []llm.ToolDef
	for _, info := range infos {
		if !allowSet[info.Name] {
			continue
		}
		if denied[info.Name] {
			continue
		}
		allowed[info.Name] = true
		tools = append(tools, llm.ToolDef{
			Type: "function",
			Function: llm.ToolDefFunc{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  info.InputSchema,
			},
		})
	}
	return tools, allowed
}

// buildSystemPrompt assembles the subagent's system prompt.
func buildSystemPrompt(prompt string) string {
	return "You are a focused task agent. Complete the following task and return your result.\n" +
		"Be concise. Do not ask clarifying questions. Use available tools if they help.\n" +
		"Task: " + prompt
}

// Execute runs a subagent conversation and returns the final output.
// It creates an LLM client for the specified profile, builds a system
// prompt, runs a tool loop, and returns the result.
func Execute(ctx context.Context, cfg Config, deps ExecutorDeps) (string, error) {
	ep := deps.Config.LLMEndpointForProfile(cfg.LLMProfile)
	if ep.BaseURL == "" || ep.Model == "" {
		return "", fmt.Errorf("LLM profile %q not found or incomplete", cfg.LLMProfile)
	}

	client := llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)

	systemPrompt := buildSystemPrompt(cfg.Prompt)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: cfg.Prompt},
	}

	// Build tool definitions from the MCP pool. Default-deny: only tools
	// explicitly listed in cfg.Tools are exposed to the subagent. DenyTools
	// is subtracted from that allowlist. Empty cfg.Tools = no MCP tools.
	// allowedTools also enforces at CallTool time — even if the LLM hallucinates
	// a tool name not in the definitions, the executor won't call it.
	var tools []llm.ToolDef
	allowedTools := make(map[string]bool)
	if deps.Pool != nil {
		tools, allowedTools = filterTools(deps.Pool.ListToolInfos(), cfg.Tools, cfg.DenyTools)
	}
	// Builtin tools (web_fetch, etc.) are also gated by the same allow/deny
	// lists. Without this branch the subagent is limited to MCP tools only,
	// which on a default Mac/Pi deploy (no MCP servers configured) means
	// ZERO tools — the model just answers from training. Wiring builtins
	// in lets `tools: ["web_fetch"]` on the spawn call actually work.
	var allowedBuiltins map[string]bool
	if len(deps.BuiltinDefs) > 0 && deps.BuiltinHandler != nil {
		var bdefs []llm.ToolDef
		bdefs, allowedBuiltins = filterBuiltinDefs(deps.BuiltinDefs, cfg.Tools, cfg.DenyTools)
		tools = append(tools, bdefs...)
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	temp := deps.Temperature
	maxTokens := deps.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}

	for i := 0; i < maxTurns; i++ {
		var msg *llm.Message
		var err error

		if len(tools) > 0 {
			msg, err = client.ChatWithTools(ctx, messages, temp, maxTokens, tools)
		} else {
			msg, err = client.ChatMessage(ctx, messages, temp, maxTokens)
		}
		if err != nil {
			return "", fmt.Errorf("subagent LLM call (turn %d): %w", i+1, err)
		}

		// No tool calls — we're done
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, *msg)

		// Execute each tool call
		for _, tc := range msg.ToolCalls {
			log.Printf("[subagent] tool_call: %s", tc.Function.Name)
			start := time.Now()
			toolText, ok := dispatchSubagentTool(ctx, tc, deps, allowedTools, allowedBuiltins, start)
			if !ok {
				toolText = fmt.Sprintf("Error: unknown tool %q", tc.Function.Name)
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    toolText,
				ToolCallID: tc.ID,
			})
		}
	}

	// Exhausted max turns — return whatever we have
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Content != "" {
			return messages[i].Content, nil
		}
	}
	return "", fmt.Errorf("subagent exhausted %d turns without producing output", maxTurns)
}

// dispatchSubagentTool routes one tool call to either the parent's builtin
// handler or the MCP pool, applying the call-time allowlist. Returns the
// stringified tool reply and ok=false when the name matches neither path
// (so the caller can emit a uniform "unknown tool" reply to the model).
func dispatchSubagentTool(
	ctx context.Context,
	tc llm.ToolCall,
	deps ExecutorDeps,
	allowedTools, allowedBuiltins map[string]bool,
	start time.Time,
) (string, bool) {
	name := tc.Function.Name

	if allowedBuiltins[name] && deps.BuiltinHandler != nil {
		out, err := deps.BuiltinHandler(ctx, name, tc.Function.Arguments)
		duration := time.Since(start)
		if err != nil {
			log.Printf("[subagent] tool_error: %s (%v, %s)", name, err, duration)
			return fmt.Sprintf("Error: %v", err), true
		}
		log.Printf("[subagent] tool_ok: %s (%s)", name, duration)
		if out == "" {
			return "OK", true
		}
		return out, true
	}

	if allowedTools[name] && deps.Pool != nil && deps.Pool.HasTool(name) {
		result, err := deps.Pool.CallTool(ctx, name, tc.Function.Arguments)
		duration := time.Since(start)
		if err != nil {
			log.Printf("[subagent] tool_error: %s (%v, %s)", name, err, duration)
			return fmt.Sprintf("Error: %v", err), true
		}
		if result != nil && len(result.Content) > 0 {
			resultJSON, _ := json.Marshal(result.Content)
			log.Printf("[subagent] tool_ok: %s (%s)", name, duration)
			return string(resultJSON), true
		}
		log.Printf("[subagent] tool_ok: %s (no output, %s)", name, duration)
		return "OK", true
	}

	log.Printf("[subagent] tool_unknown: %s", name)
	return "", false
}
