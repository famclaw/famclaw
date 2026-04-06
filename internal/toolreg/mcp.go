package toolreg

import "github.com/famclaw/famclaw/internal/mcp"

// LoadFromMCPPool registers all tools from an MCP pool into the registry.
// Tools are namespaced as mcp__<server>__<tool>.
func (r *Registry) LoadFromMCPPool(pool *mcp.Pool) int {
	if pool == nil {
		return 0
	}
	count := 0
	for _, info := range pool.ListToolInfos() {
		r.Register(&Tool{
			Name:        ToolName("mcp", info.ServerName, info.Name),
			Description: info.Description,
			InputSchema: info.InputSchema,
			Source:      "mcp",
			ServerName:  info.ServerName,
		})
		count++
	}
	return count
}
