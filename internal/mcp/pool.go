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
// Disabled servers and invalid configs are skipped with a log message.
func (p *Pool) RegisterFromConfig(servers map[string]config.MCPServerConfig) {
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
		p.clients[name] = &managedClient{
			client: NewTransportClient(name, cfg),
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
	for toolName, mc := range toolAliases {
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

	result, err := mc.client.CallTool(ctx, name, args)
	if err == nil {
		mc.restartCnt = 0
		return result, nil
	}

	if mc.restartCnt < 1 {
		log.Printf("[mcp-pool] tool %q failed, restarting %s: %v", name, mc.name, err)
		mc.restartCnt++
		mc.client.Stop()
		mc.client = NewTransportClient(mc.name, mc.cfg)
		if startErr := mc.client.Start(ctx); startErr != nil {
			return nil, fmt.Errorf("restarting MCP server %q for tool %q: %w", mc.name, name, startErr)
		}
		return mc.client.CallTool(ctx, name, args)
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
