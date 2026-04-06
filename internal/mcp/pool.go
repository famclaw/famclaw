package mcp

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/config"
)

// Pool manages multiple MCP clients (one per server), with lazy start and auto-restart.
type Pool struct {
	clients map[string]*managedClient // server name + tool name → client
	mu      sync.RWMutex
}

type managedClient struct {
	mu         sync.Mutex // guards client, restartCnt
	client     *Client
	name       string
	cfg        config.MCPServerConfig
	restartCnt int
}

// NewPool creates an empty MCP pool.
func NewPool() *Pool {
	return &Pool{
		clients: make(map[string]*managedClient),
	}
}

// RegisterFromConfig registers MCP servers from config.
// Credentials are per-skill env vars injected at process spawn time.
func (p *Pool) RegisterFromConfig(servers map[string]config.MCPServerConfig, credentials map[string]map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for name, cfg := range servers {
		if cfg.Disabled {
			continue
		}
		if err := config.ValidateMCPServer(name, cfg); err != nil {
			log.Printf("[mcp-pool] skip %s: %v", name, err)
			continue
		}
		c := NewTransportClient(name, cfg)
		if creds, ok := credentials[name]; ok {
			c.env = creds
		}
		p.clients[name] = &managedClient{
			client: c,
			name:   name,
			cfg:    cfg,
		}
	}
}

// StartAll starts all registered MCP servers and maps their tools by name.
// Failed servers are kept in the map for later retry — not removed.
func (p *Pool) StartAll(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	toolAliases := make(map[string]*managedClient)
	for name, mc := range p.clients {
		if err := mc.client.Start(ctx); err != nil {
			log.Printf("[mcp-pool] failed to start %s: %v", name, err)
			continue
		}
		for _, tool := range mc.client.Tools() {
			toolAliases[tool.Name] = mc
		}
	}
	// Add tool-name aliases, but never overwrite a server entry
	for toolName, mc := range toolAliases {
		if _, isServer := p.clients[toolName]; isServer {
			log.Printf("[mcp-pool] tool %q shadows server name — skipping alias", toolName)
			continue
		}
		p.clients[toolName] = mc
	}
	return nil
}

// CallTool calls a tool by name, lazily starting the server if needed.
// Restarts failed servers once. Resets restart count on success.
func (p *Pool) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	p.mu.RLock()
	mc, ok := p.clients[name]
	p.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Lazy start: if server failed at boot or hasn't started yet
	if mc.client.inner == nil {
		if err := mc.client.Start(ctx); err != nil {
			return nil, fmt.Errorf("starting MCP server %q for tool %q: %w", mc.name, name, err)
		}
	}

	result, err := mc.client.CallTool(ctx, name, args)
	if err == nil {
		mc.restartCnt = 0
		return result, nil
	}

	// Auto-restart once on failure
	if mc.restartCnt < 1 {
		log.Printf("[mcp-pool] tool %q failed, restarting %s: %v", name, mc.name, err)
		mc.restartCnt++
		mc.client.Stop()
		mc.client = NewTransportClient(mc.name, mc.cfg)
		if startErr := mc.client.Start(ctx); startErr != nil {
			return nil, fmt.Errorf("restarting MCP server %q for tool %q: %w", mc.name, name, startErr)
		}
		result, err = mc.client.CallTool(ctx, name, args)
		if err == nil {
			mc.restartCnt = 0
		}
		return result, err
	}

	return nil, err
}

// HasTool returns true if the pool has a handler for the given tool.
func (p *Pool) HasTool(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.clients[name]
	return ok
}

// ListTools returns all available tool names.
func (p *Pool) ListTools() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool)
	var names []string
	for _, mc := range p.clients {
		for _, tool := range mc.client.Tools() {
			if !seen[tool.Name] {
				seen[tool.Name] = true
				names = append(names, tool.Name)
			}
		}
	}
	return names
}

// ToolInfo describes an MCP tool with its schema for registration in the tool registry.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]any
	ServerName  string
}

// ListToolInfos returns tool metadata for all available tools.
func (p *Pool) ListToolInfos() []ToolInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	seen := make(map[string]bool)
	var infos []ToolInfo
	for serverName, mc := range p.clients {
		for _, tool := range mc.client.Tools() {
			if seen[tool.Name] {
				continue
			}
			seen[tool.Name] = true
			schema := make(map[string]any)
			if tool.InputSchema.Properties != nil {
				schema["type"] = "object"
				schema["properties"] = tool.InputSchema.Properties
				if len(tool.InputSchema.Required) > 0 {
					schema["required"] = tool.InputSchema.Required
				}
			}
			infos = append(infos, ToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: schema,
				ServerName:  serverName,
			})
		}
	}
	return infos
}

// StopAll terminates all MCP server connections.
func (p *Pool) StopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	stopped := make(map[*Client]bool)
	for _, mc := range p.clients {
		if !stopped[mc.client] {
			mc.client.Stop()
			stopped[mc.client] = true
		}
	}
}

// MaxToolCallIterations is the hard limit for tool call loops.
const MaxToolCallIterations = 10
