package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
)

const toolNameMCPRemove = "builtin__mcp_remove"

func MCPRemoveDefinition() agentcore.Tool {
	return agentcore.Tool{Name: toolNameMCPRemove, Description: "Remove a configured MCP server (parent-only). This stops the server and removes it from configuration.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "description": "Name of the MCP server to remove."}}, "required": []string{"name"}}, Source: "builtin", Roles: []string{"parent"}}
}

func HandleMCPRemove(ctx context.Context, deps Deps, args map[string]any) (string, error) {
	if deps.Cfg == nil {
		return "", fmt.Errorf("mcp_remove: config not available")
	}
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("mcp_remove: name is required")
	}
	if _, exists := deps.Cfg.Skills.MCPServers[name]; !exists {
		return "", fmt.Errorf("mcp_remove: server %q is not configured", name)
	}
	old := deps.Cfg.Skills.MCPServers[name]
	delete(deps.Cfg.Skills.MCPServers, name)
	if deps.ConfigPath != "" {
		if err := deps.Cfg.Save(deps.ConfigPath); err != nil {
			deps.Cfg.Skills.MCPServers[name] = old
			return "", fmt.Errorf("mcp_remove: saving config: %w", err)
		}
	}
	if deps.MCP != nil {
		_ = deps.MCP.ReloadConfig(deps.Cfg)
	}
	auditArgs, _ := json.Marshal(map[string]string{"name": name})
	if err := logAudit(ctx, deps, toolNameMCPRemove, json.RawMessage(auditArgs)); err != nil {
		log.Printf("[admin] audit log failed for %s: %v", toolNameMCPRemove, err)
	}
	return fmt.Sprintf("MCP server %q removed.", name), nil
}
