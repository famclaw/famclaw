package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterInProcessServer registers an in-memory MCP server (a
// *server.MCPServer) into the pool under the given name. This is the
// test/verification seam for the tool loop: an agentcore test can stand
// up a fake android-control-style server that returns image content and
// drive the whole tool-loop path (Pool.HasTool → Pool.CallTool →
// content split → vision describe) without spawning a subprocess or
// needing a real device. Production servers are registered from config
// (stdio/HTTP/SSE); nothing in production uses this seam.
//
// The handshake and tool listing happen immediately so the pool sees the
// server's tools as soon as the call returns, matching RegisterFromConfig
// + StartAll semantics.
func (p *Pool) RegisterInProcessServer(name string, srv *server.MCPServer) error {
	inner, err := client.NewInProcessClient(srv)
	if err != nil {
		return fmt.Errorf("creating in-process MCP client %q: %w", name, err)
	}

	ctx := context.Background()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "famclaw", Version: "1.0.0"}
	if _, err := inner.Initialize(ctx, initReq); err != nil {
		inner.Close()
		return fmt.Errorf("MCP initialize %q: %w", name, err)
	}
	toolsResult, err := inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		inner.Close()
		return fmt.Errorf("MCP tools/list %q: %w", name, err)
	}

	c := &Client{
		name:          name,
		transportType: "inprocess",
		SandboxRoot:   p.SandboxRoot,
		inner:         inner,
		tools:         toolsResult.Tools,
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.clients[name]; exists {
		return fmt.Errorf("MCP server %q already registered", name)
	}
	mc := &managedClient{client: c, name: name}
	p.clients[name] = mc
	for _, tool := range toolsResult.Tools {
		if _, isServer := p.clients[tool.Name]; isServer {
			continue
		}
		p.clients[tool.Name] = mc
	}
	return nil
}
