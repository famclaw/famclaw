package admin

import (
	"context"
	"fmt"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameMCPList = "builtin__mcp_list"

// MCPListDefinition returns the parent-only tool that lists all configured
// MCP (Model Context Protocol) servers. Each entry shows the server name
// and its transport type (stdio/http/sse).
func MCPListDefinition() agentcore.Tool {
	return agentcore.Tool{
		Name: toolNameMCPList,
		Description: "List all configured MCP servers (parent-only). " +
			"Returns the name and transport of each MCP server that has been " +
			"added via mcp_add or the dashboard. Use this to check which " +
			"servers are available before trying to use their tools.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{},
			"required": []string{},
		},
		Source: "builtin",
		Roles:  []string{"parent"},
	}
}

// HandleMCPList dispatches builtin__mcp_list.
func HandleMCPList(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.Cfg == nil {
		return "", fmt.Errorf("mcp_list: config not available")
	}

	servers := deps.Cfg.Skills.MCPServers
	if len(servers) == 0 {
		return "No MCP servers configured.", nil
	}

	// Sort names for deterministic output.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	// Simple insertion sort (small n, no dependency on sort package here).
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}

	var result string
	result = fmt.Sprintf("Configured MCP servers (%d):\n", len(names))
	for _, name := range names {
		cfg := servers[name]
		transport := cfg.Transport
		if transport == "" {
			if cfg.Command != "" {
				transport = "stdio"
			} else if cfg.URL != "" {
				transport = "http"
			}
		}
		status := "enabled"
		if cfg.Disabled {
			status = "disabled"
		}
		result += fmt.Sprintf("  • %s — transport=%s, status=%s\n", name, transport, status)
	}
	return result, nil
}
