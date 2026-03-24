package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// Pool manages multiple MCP clients (one per skill), with lazy start and auto-restart.
type Pool struct {
	clients map[string]*managedClient // tool name → client
	mu      sync.RWMutex
}

type managedClient struct {
	client     *Client
	cmd        string
	args       []string
	restartCnt int
}

// NewPool creates an empty MCP pool.
func NewPool() *Pool {
	return &Pool{
		clients: make(map[string]*managedClient),
	}
}

// Register adds an MCP server to the pool.
// Tools from this server become available by name.
func (p *Pool) Register(cmd string, args ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	client := NewClient(cmd, args...)
	mc := &managedClient{
		client: client,
		cmd:    cmd,
		args:   args,
	}

	// Store by command name as key until tools are discovered
	p.clients[cmd] = mc
}

// StartAll starts all registered MCP servers and maps their tools.
func (p *Pool) StartAll(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Start each client and remap by tool name
	newClients := make(map[string]*managedClient)
	for key, mc := range p.clients {
		if err := mc.client.Start(ctx); err != nil {
			log.Printf("[mcp-pool] failed to start %s: %v", key, err)
			continue
		}
		for _, tool := range mc.client.Tools() {
			newClients[tool.Name] = mc
		}
		// Keep the original key mapping too
		newClients[key] = mc
	}
	p.clients = newClients
	return nil
}

// CallTool calls a tool by name, lazily starting the server if needed.
// Restarts crashed servers once automatically.
func (p *Pool) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	p.mu.RLock()
	mc, ok := p.clients[name]
	p.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}

	result, err := mc.client.CallTool(ctx, name, args)
	if err != nil && mc.restartCnt < 1 {
		// Auto-restart once
		log.Printf("[mcp-pool] tool %q failed, restarting server: %v", name, err)
		mc.restartCnt++
		mc.client.Stop()
		mc.client = NewClient(mc.cmd, mc.args...)
		if startErr := mc.client.Start(ctx); startErr != nil {
			return nil, fmt.Errorf("restarting MCP server for %q: %w", name, startErr)
		}
		return mc.client.CallTool(ctx, name, args)
	}

	return result, err
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

// StopAll terminates all MCP server processes.
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
