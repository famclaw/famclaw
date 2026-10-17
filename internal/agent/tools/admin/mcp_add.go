package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/honeybadger"
)

const toolNameMCPAdd = "builtin__mcp_add"

// MCPAddDefinition returns the parent-only tool that registers a new MCP
// (Model Context Protocol) server. Installing an MCP server is arbitrary
// code execution (the server can run any command or expose any tool), so
// this tool is adult-gated (parent role only) and routed through the same
// security-gate pattern skills use — the server binary/command is subject
// to sandboxing and scanning just like any other MCP child process.
func MCPAddDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameMCPAdd,
		Description: "Register a new MCP (Model Context Protocol) server (parent-only). " +
			"An MCP server exposes tools to the assistant. Specify a 'name' for " +
			"the server, and either a stdio 'command' (+ optional 'args') or " +
			"an 'url' for http/sse transport. The server will be saved to " +
			"config; if its connection succeeds, its tools become available immediately. " +
			"Example: mcp_add(name=\"playwright\", command=\"npx\", args=\"-y @modelcontextprotocol/server-playwright\")",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "Unique name for the server (e.g. 'playwright')."},
				"command":   map[string]any{"type": "string", "description": "Command to run for stdio transport (e.g. 'npx')."},
				"args":      map[string]any{"type": "string", "description": "Space-separated arguments for the command (e.g. '-y @modelcontextprotocol/server-playwright')."},
				"url":       map[string]any{"type": "string", "description": "URL for http/sse transport."},
				"transport": map[string]any{"type": "string", "description": "Optional: 'stdio', 'http', or 'sse'. Auto-detected from command/url if omitted."},
			},
			"required": []string{"name"},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleMCPAdd dispatches builtin__mcp_add.
func HandleMCPAdd(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.Cfg == nil {
		return "", fmt.Errorf("mcp_add: config not available")
	}

	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("mcp_add: name is required")
	}

	command, _ := args["command"].(string)
	url, _ := args["url"].(string)
	transport, _ := args["transport"].(string)

	// Parse args: accept either a space-separated string or a JSON array.
	var mcpArgs []string
	if rawArgs, ok := args["args"]; ok {
		switch v := rawArgs.(type) {
		case string:
			mcpArgs = strings.Fields(v)
		case []any:
			mcpArgs = make([]string, 0, len(v))
			for _, a := range v {
				if s, ok := a.(string); ok {
					mcpArgs = append(mcpArgs, s)
				}
			}
		}
	}

	// Build the MCPServerConfig.
	mcpCfg := config.MCPServerConfig{
		Transport: transport,
		Command:   strings.TrimSpace(command),
		Args:      mcpArgs,
		URL:       strings.TrimSpace(url),
	}

	// Normalize transport detection (mirrors config.ValidateMCPServer).
	if mcpCfg.Transport == "" {
		if mcpCfg.Command != "" {
			mcpCfg.Transport = "stdio"
		} else if mcpCfg.URL != "" {
			mcpCfg.Transport = "http"
		}
	}

	if err := config.ValidateMCPServer(name, mcpCfg); err != nil {
		return "", fmt.Errorf("mcp_add: %w", err)
	}

	// Check for duplicate.
	if _, exists := deps.Cfg.Skills.MCPServers[name]; exists {
		return "", fmt.Errorf("mcp_add: an MCP server named %q already exists", name)
	}

	// Scan the executable/endpoint before persisting. MCP servers can execute code.
	if deps.Cfg.SecCheck.Enabled {
		if deps.Scanner == nil || !deps.Scanner.Available() {
			return "", fmt.Errorf("mcp_add: install refused: honeybadger scanner is unavailable")
		}
		target := mcpCfg.URL
		offline := false
		if target == "" {
			target = mcpCfg.Command
			offline = true
		}
		result, err := deps.Scanner.Scan(ctx, target, honeybadger.ScanOptions{Paranoia: deps.Cfg.SecCheck.Paranoia, Offline: offline})
		if err != nil {
			return "", fmt.Errorf("mcp_add: security scan failed: %w", err)
		}
		if result == nil || result.Verdict == "SKIP" {
			return "", fmt.Errorf("mcp_add: security scan did not produce a verdict")
		}
		if result.Verdict == "FAIL" && deps.Cfg.SecCheck.BlockOnFail {
			return "", fmt.Errorf("mcp_add: server rejected by security scan: %s", result.Reasoning)
		}
	}

	// Add to in-memory config.
	if deps.Cfg.Skills.MCPServers == nil {
		deps.Cfg.Skills.MCPServers = make(map[string]config.MCPServerConfig)
	}
	deps.Cfg.Skills.MCPServers[name] = mcpCfg

	// Persist to disk if we have a config path.
	if deps.ConfigPath != "" {
		if err := deps.Cfg.Save(deps.ConfigPath); err != nil {
			// Roll back the in-memory change so we don't report success.
			delete(deps.Cfg.Skills.MCPServers, name)
			return "", fmt.Errorf("mcp_add: saving config: %w", err)
		}
	} else {
		log.Printf("[admin] mcp_add: no config path set, skipping persistence for %q", name)
	}

	// Reload the MCP pool so the new server is registered and its tools
	// become available on the next turn. The server is already saved to
	// config (and that persists regardless of reload outcome) — but if the
	// pool can't load it, the user needs to know so they aren't surprised
	// that the tools are missing. See issue #312 / honest-failure work.
	poolNote := ""
	if deps.MCP != nil {
		results := deps.MCP.ReloadConfig(deps.Cfg)
		if err, ok := results[name]; ok && err != nil {
			poolNote = fmt.Sprintf(" Start failed: %v. The server is saved to config.", err)
		} else if _, ok := results[name]; ok {
			poolNote = " Server started successfully."
		} else if len(results) == 0 {
			poolNote = " Server configuration reloaded."
		}
		for server, err := range results {
			if err != nil {
				log.Printf("[admin] mcp_add: pool start failed for %q: %v", server, err)
			}
		}
	} else {
		log.Printf("[admin] mcp_add: no MCP pool configured, server added to config only")
		poolNote = " No MCP pool is running; the server will be loaded on next startup."
	}

	// Audit log.
	auditArgs, _ := json.Marshal(map[string]any{
		"name":      name,
		"transport": mcpCfg.Transport,
		"command":   mcpCfg.Command,
		"args":      mcpArgs,
		"url":       mcpCfg.URL,
	})
	if err := logAudit(ctx, deps, toolNameMCPAdd, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameMCPAdd, err)
	}

	return fmt.Sprintf("✅ MCP server %q registered (transport=%s).%s", name, mcpCfg.Transport, poolNote), nil
}
