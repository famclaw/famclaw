// Package mcp provides MCP client management for FamClaw skill tool servers.
// Supports stdio, HTTP (StreamableHTTP), and SSE transports via mcp-go.
package mcp

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/config"
)

// Client wraps an mcp-go client for any transport (stdio, HTTP, SSE).
type Client struct {
	name          string
	transportType string // stdio | http | sse | inprocess (test only)
	cfg           config.MCPServerConfig
	env           map[string]string // per-skill credential env vars
	inner         client.MCPClient
	tools         []mcp.Tool
	closed        bool
}

// NewTransportClient creates an MCP client from config.
// Transport is auto-detected from fields if not set explicitly:
//   - command present → stdio
//   - url present → http
func NewTransportClient(name string, cfg config.MCPServerConfig) *Client {
	t := cfg.Transport
	if t == "" {
		if cfg.Command != "" {
			t = "stdio"
		} else if cfg.URL != "" {
			t = "http"
		}
	}
	return &Client{name: name, transportType: t, cfg: cfg}
}

// Start connects to the MCP server and performs the initialize handshake.
// For stdio: spawns the process. For HTTP/SSE: connects to the remote server.
func (c *Client) Start(ctx context.Context) error {
	if c.inner != nil {
		return nil
	}

	// Timeout for connection — remote servers may be slow or unreachable
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var inner client.MCPClient
	var err error

	switch c.transportType {
	case "stdio":
		// Inject per-skill credentials as env vars (never appear in LLM context)
		var env []string
		if len(c.env) > 0 {
			env = os.Environ()
			for k, v := range c.env {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
		}
		inner, err = client.NewStdioMCPClient(c.cfg.Command, env, c.cfg.Args...)
	case "http":
		var opts []transport.StreamableHTTPCOption
		if len(c.cfg.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(c.cfg.Headers))
		}
		inner, err = client.NewStreamableHttpClient(c.cfg.URL, opts...)
	case "sse":
		var opts []transport.ClientOption
		if len(c.cfg.Headers) > 0 {
			opts = append(opts, transport.WithHeaders(c.cfg.Headers))
		}
		inner, err = client.NewSSEMCPClient(c.cfg.URL, opts...)
	default:
		return fmt.Errorf("unknown MCP transport %q for server %q", c.transportType, c.name)
	}
	if err != nil {
		return fmt.Errorf("creating MCP client %q (%s): %w", c.name, c.transportType, err)
	}
	c.inner = inner

	// Initialize handshake
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "famclaw", Version: "1.0.0"}

	_, err = c.inner.Initialize(startCtx, initReq)
	if err != nil {
		c.inner.Close()
		c.inner = nil
		return fmt.Errorf("MCP initialize %q: %w", c.name, err)
	}

	// List available tools
	toolsResult, err := c.inner.ListTools(startCtx, mcp.ListToolsRequest{})
	if err != nil {
		log.Printf("[mcp] tools/list failed for %q: %v", c.name, err)
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
		return nil, fmt.Errorf("calling tool %q on %q: %w", name, c.name, err)
	}
	return result, nil
}

// Tools returns the list of available tools.
func (c *Client) Tools() []mcp.Tool { return c.tools }

// Stop terminates the MCP server connection.
func (c *Client) Stop() {
	if c.inner != nil && !c.closed {
		c.inner.Close()
		c.closed = true
		c.inner = nil
	}
}
