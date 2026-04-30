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
	OAuthStore  *llm.OAuthStore
	Temperature float64
	MaxTokens   int
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

// buildSystemPrompt assembles the subagent's system prompt. When the endpoint
// uses OAuth (Anthropic), it prepends the ClaudeCodeSystemPrefix expected by
// the API. Exported via lowercase so tests in the same package can exercise it.
func buildSystemPrompt(prompt, authType string) string {
	systemPrompt := "You are a focused task agent. Complete the following task and return your result.\n" +
		"Be concise. Do not ask clarifying questions. Use available tools if they help.\n" +
		"Task: " + prompt
	if authType == "oauth" {
		systemPrompt = llm.ClaudeCodeSystemPrefix + "\n\n" + systemPrompt
	}
	return systemPrompt
}

// Execute runs a subagent conversation and returns the final output.
// It creates an LLM client for the specified profile, builds a system
// prompt, runs a tool loop, and returns the result.
func Execute(ctx context.Context, cfg Config, deps ExecutorDeps) (string, error) {
	ep := deps.Config.LLMEndpointForProfile(cfg.LLMProfile)
	if ep.BaseURL == "" || ep.Model == "" {
		return "", fmt.Errorf("LLM profile %q not found or incomplete", cfg.LLMProfile)
	}

	var client *llm.Client
	if ep.AuthType == "oauth" && deps.OAuthStore != nil {
		client = llm.NewOAuthClient(ep.BaseURL, ep.Model, deps.OAuthStore, "anthropic")
	} else {
		client = llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
	}

	systemPrompt := buildSystemPrompt(cfg.Prompt, ep.AuthType)

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

			// Enforce allowlist at call time — even if the LLM hallucinates
			// a tool not in the definitions, don't let it through.
			if !allowedTools[tc.Function.Name] || deps.Pool == nil || !deps.Pool.HasTool(tc.Function.Name) {
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    fmt.Sprintf("Error: unknown tool %q", tc.Function.Name),
					ToolCallID: tc.ID,
				})
				continue
			}

			result, err := deps.Pool.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
			duration := time.Since(start)

			var toolText string
			if err != nil {
				toolText = fmt.Sprintf("Error: %v", err)
				log.Printf("[subagent] tool_error: %s (%v, %s)", tc.Function.Name, err, duration)
			} else if result != nil && len(result.Content) > 0 {
				resultJSON, _ := json.Marshal(result.Content)
				toolText = string(resultJSON)
				log.Printf("[subagent] tool_ok: %s (%s)", tc.Function.Name, duration)
			} else {
				toolText = "OK"
				log.Printf("[subagent] tool_ok: %s (no output, %s)", tc.Function.Name, duration)
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
