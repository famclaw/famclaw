package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/famclaw/famclaw/internal/config"
)

// handleMCPCommand processes parent-only MCP server management commands.
func (r *Router) handleMCPCommand(ctx context.Context, actor string, fields []string) Reply {
	if len(fields) < 2 {
		return Reply{Text: "MCP server management: mcp list | mcp add <name> <transport> [options] | mcp remove <name>", PolicyAction: "mcp"}
	}

	cmd := strings.ToLower(fields[1])
	switch cmd {
	case "list":
		servers := r.listMCPServerNames()
		if len(servers) == 0 {
			return Reply{Text: "No MCP servers configured.", PolicyAction: "mcp"}
		}
		return Reply{Text: "Configured MCP servers:\n" + strings.Join(servers, "\n"), PolicyAction: "mcp"}

	case "add":
		if len(fields) < 3 {
			return Reply{Text: "Usage: mcp add <name> <transport> [options]", PolicyAction: "mcp"}
		}
		name := fields[2]
		if len(fields) < 4 {
			return Reply{Text: "Usage: mcp add <name> <transport> [options]", PolicyAction: "mcp"}
		}
		transport := fields[3]
		// Parse remaining fields as key=value options
		opts := make(map[string]string)
		for i := 4; i < len(fields); i++ {
			parts := strings.SplitN(fields[i], "=", 2)
			if len(parts) != 2 {
				return Reply{Text: fmt.Sprintf("Invalid option %q: expected KEY=VALUE", fields[i]), PolicyAction: "error"}
			}
			opts[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
		var cfg config.MCPServerConfig
		cfg.Transport = transport
		if transport == "stdio" {
			cfg.Command = opts["command"]
			if argsStr := opts["args"]; argsStr != "" {
				// Parse args as comma-separated list
				var args []string
				for _, arg := range strings.Split(argsStr, ",") {
					if arg = strings.TrimSpace(arg); arg != "" {
						args = append(args, arg)
					}
				}
				cfg.Args = args
			}
		} else if transport == "http" || transport == "sse" {
			cfg.URL = opts["url"]
			// Headers: comma-separated key=value
			headersStr := opts["headers"]
			if headersStr != "" {
				headers := make(map[string]string)
				for _, pair := range strings.Split(headersStr, ",") {
					parts := strings.SplitN(pair, "=", 2)
					if len(parts) != 2 {
						return Reply{Text: fmt.Sprintf("Invalid header %q: expected KEY=VALUE", pair), PolicyAction: "error"}
					}
					headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
				cfg.Headers = headers
			}
		}
		if disabledStr := opts["disabled"]; disabledStr != "" {
			if disabled, err := strconv.ParseBool(disabledStr); err != nil {
				return Reply{Text: fmt.Sprintf("Invalid disabled value %q: expected boolean", disabledStr), PolicyAction: "error"}
			} else {
				cfg.Disabled = disabled
			}
		}
		// Validate the config
		if err := config.ValidateMCPServer(name, cfg); err != nil {
			return Reply{Text: fmt.Sprintf("Invalid MCP server config: %v", err), PolicyAction: "error"}
		}
		if err := r.addMCPServer(name, cfg); err != nil {
			return Reply{Text: fmt.Sprintf("Failed to add MCP server: %v", err), PolicyAction: "error"}
		}
		return Reply{Text: fmt.Sprintf("MCP server %q added.", name), PolicyAction: "mcp"}

	case "remove":
		if len(fields) < 3 {
			return Reply{Text: "Usage: mcp remove <name>", PolicyAction: "mcp"}
		}
		name := fields[2]
		if err := r.removeMCPServer(name); err != nil {
			return Reply{Text: fmt.Sprintf("Failed to remove MCP server: %v", err), PolicyAction: "error"}
		}
		return Reply{Text: fmt.Sprintf("MCP server %q removed.", name), PolicyAction: "mcp"}

	default:
		return Reply{Text: "Unknown MCP command: " + fields[1] + ". Try: list, add, remove", PolicyAction: "mcp"}
	}
}

// listMCPServerNames returns a slice of formatted strings representing the configured MCP servers.
func (r *Router) listMCPServerNames() []string {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	var names []string
	for name, cfg := range r.cfg.Skills.MCPServers {
		var desc string
		if cfg.Disabled {
			desc = "(disabled)"
		} else {
			switch cfg.Transport {
			case "stdio":
				desc = fmt.Sprintf("stdio: %s", cfg.Command)
			case "http", "sse":
				desc = fmt.Sprintf("%s://%s", cfg.Transport, cfg.URL)
			}
		}
		names = append(names, fmt.Sprintf("- %s: %s %s", name, cfg.Transport, desc))
	}
	return names
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
	delete(r.cfg.Skills.MCPServers, name)
	return r.cfg.Save(r.configPath)
}
