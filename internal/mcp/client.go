// Package mcp provides MCP client management for FamClaw skill tool servers.
// Uses github.com/mark3labs/mcp-go for the MCP protocol implementation.
package mcp

import (
	"context"
	"fmt"
	"log"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Client wraps an mcp-go stdio client for a single MCP server.
type Client struct {
	cmd    string
	args   []string
	inner  client.MCPClient
	tools  []mcp.Tool
	closed bool
}

// NewClient creates an MCP client that will spawn the given command.
// The process is started lazily on first use.
func NewClient(cmd string, args ...string) *Client {
	return &Client{cmd: cmd, args: args}
}

// Start spawns the MCP server process and performs the initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	if c.inner != nil {
		return nil
	}

	inner, err := client.NewStdioMCPClient(c.cmd, nil, c.args...)
	if err != nil {
		return fmt.Errorf("creating MCP client for %q: %w", c.cmd, err)
	}
	c.inner = inner

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "famclaw",
		Version: "1.0.0",
	}

	_, err = c.inner.Initialize(ctx, initReq)
	if err != nil {
		c.inner.Close()
		c.inner = nil
		return fmt.Errorf("MCP initialize %q: %w", c.cmd, err)
	}

	// List available tools
	toolsResult, err := c.inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		log.Printf("[mcp] tools/list failed for %q: %v", c.cmd, err)
	} else {
		c.tools = toolsResult.Tools
	}

	return nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if c.inner == nil {
		if err := c.Start(ctx); err != nil {
			return nil, err
		}
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	result, err := c.inner.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("calling tool %q: %w", name, err)
	}
	return result, nil
}

// Tools returns the list of available tools.
func (c *Client) Tools() []mcp.Tool {
	return c.tools
}

// Stop terminates the MCP server process.
func (c *Client) Stop() {
	if c.inner != nil && !c.closed {
		c.inner.Close()
		c.closed = true
		c.inner = nil
	}
}
