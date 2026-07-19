package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/famclaw/famclaw/internal/config"
)

// handleMCPCommand handles MCP management commands from chat.
func (r *Router) handleMCPCommand(ctx context.Context, actor string, fields []string) Reply {
	user := r.cfg.GetUser(actor)
	if user == nil || user.Role != "parent" {
		return Reply{PolicyAction: "block", Text: "Only a parent can manage MCP servers."}
	}
	if len(fields) == 0 || fields[0] != "mcp" {
		return Reply{PolicyAction: "skip"}
	}
	if len(fields) == 1 {
		return r.listMCPServerNames()
	}
	switch fields[1] {
	case "list":
		return r.listMCPServerNames()
	case "add":
		if len(fields) < 4 {
			return Reply{PolicyAction: "error", Text: "Usage: mcp add <name> <transport> <key=value>..."}
		}
		name := fields[2]
		transport := fields[3]
		kvs := fields[4:]
		cfg := config.MCPServerConfig{}
		for _, kv := range kvs {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				return Reply{PolicyAction: "error", Text: fmt.Sprintf("Invalid key=value: %s", kv)}
			}
			key := parts[0]
			value := parts[1]
			switch key {
			case "command":
				cfg.Command = value
			case "args":
				cfg.Args = strings.Split(value, ",")
			case "url":
				cfg.URL = value
			case "headers":
				// headers are comma-separated key=value pairs
				for _, header := range strings.Split(value, ",") {
					h := strings.SplitN(header, "=", 2)
					if len(h) != 2 {
						return Reply{PolicyAction: "error", Text: fmt.Sprintf("Invalid header: %s", header)}
					}
					cfg.Headers[h[0]] = h[1]
				}
			case "disabled":
				if value == "true" {
					cfg.Disabled = true
				} else if value == "false" {
					cfg.Disabled = false
				} else {
					return Reply{PolicyAction: "error", Text: fmt.Sprintf("Invalid disabled value: %s", value)}
				}
			default:
				return Reply{PolicyAction: "error", Text: fmt.Sprintf("Unknown key: %s", key)}
			}
		}
		cfg.Transport = transport
		if err := config.ValidateMCPServer(name, cfg); err != nil {
			return Reply{PolicyAction: "error", Text: fmt.Sprintf("Invalid MCP server config: %s", err)}
		}
		if err := r.addMCPServer(name, cfg); err != nil {
			return Reply{PolicyAction: "error", Text: fmt.Sprintf("Failed to add MCP server: %v", err)}
		}
		return Reply{PolicyAction: "mcp", Text: fmt.Sprintf("MCP server \"%s\" added.", name)}
	case "remove":
		if len(fields) != 3 {
			return Reply{PolicyAction: "error", Text: "Usage: mcp remove <name>"}
		}
		name := fields[2]
		if err := r.removeMCPServer(name); err != nil {
			return Reply{PolicyAction: "error", Text: fmt.Sprintf("Failed to remove MCP server: %v", err)}
		}
		return Reply{PolicyAction: "mcp", Text: fmt.Sprintf("MCP server \"%s\" removed.", name)}
	default:
		return Reply{PolicyAction: "error", Text: fmt.Sprintf("Unknown MCP subcommand: %s", fields[1])}
	}
}

// listMCPServerNames returns a formatted list of configured MCP server names.
func (r *Router) listMCPServerNames() Reply {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	if len(r.cfg.Skills.MCPServers) == 0 {
		return Reply{PolicyAction: "mcp", Text: "No MCP servers configured."}
	}
	names := make([]string, 0, len(r.cfg.Skills.MCPServers))
	for name := range r.cfg.Skills.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return Reply{PolicyAction: "mcp", Text: "Configured MCP servers: " + strings.Join(names, ", ")}
}

// addMCPServer adds an MCP server to the config and persists the change.
func (r *Router) addMCPServer(name string, cfg config.MCPServerConfig) error {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	if r.cfg.Skills.MCPServers == nil {
		r.cfg.Skills.MCPServers = make(map[string]config.MCPServerConfig)
	}
	r.cfg.Skills.MCPServers[name] = cfg
	return r.cfg.Save(r.configPath)
}

// removeMCPServer removes an MCP server from the config and persists the change.
func (r *Router) removeMCPServer(name string) error {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	if r.cfg.Skills.MCPServers == nil {
		return nil // nothing to remove
	}
	delete(r.cfg.Skills.MCPServers, name)
	if len(r.cfg.Skills.MCPServers) == 0 {
		r.cfg.Skills.MCPServers = nil
	}
	return r.cfg.Save(r.configPath)
}
