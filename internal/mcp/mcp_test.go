package mcp

import (
	"context"
	"fmt"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/famclaw/famclaw/internal/config"
)

func newMockServer() *server.MCPServer {
	s := server.NewMCPServer("mock-server", "1.0.0",
		server.WithToolCapabilities(true),
	)

	s.AddTool(mcp.NewTool("echo",
		mcp.WithDescription("Echo input text"),
		mcp.WithString("text", mcp.Description("Text to echo")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text := req.GetString("text", "")
		return mcp.NewToolResultText(text), nil
	})

	s.AddTool(mcp.NewTool("add",
		mcp.WithDescription("Add two numbers"),
		mcp.WithNumber("a", mcp.Description("First number")),
		mcp.WithNumber("b", mcp.Description("Second number")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a := req.GetFloat("a", 0)
		b := req.GetFloat("b", 0)
		return mcp.NewToolResultText(fmt.Sprintf("%d", int(a+b))), nil
	})

	return s
}

// newTestClient creates a Client backed by an in-process mock MCP server.
func newTestClient(t *testing.T) *Client {
	t.Helper()

	s := newMockServer()
	inner, err := mcpclient.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { inner.Close() })

	ctx := context.Background()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "1.0"}
	_, err = inner.Initialize(ctx, initReq)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	toolsResult, err := inner.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	return &Client{
		name:          "mock",
		transportType: "inprocess",
		inner:         inner,
		tools:         toolsResult.Tools,
	}
}

// ── Transport constructor tests ──────────────────────────────────────────────

func TestNewTransportClient_Stdio(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "echo"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "stdio" {
		t.Errorf("transport = %q, want stdio", c.transportType)
	}
}

func TestNewTransportClient_HTTP(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://localhost:9999/mcp"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "http" {
		t.Errorf("transport = %q, want http", c.transportType)
	}
}

func TestNewTransportClient_SSE(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "sse", URL: "http://localhost:9999/sse"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "sse" {
		t.Errorf("transport = %q, want sse", c.transportType)
	}
}

func TestNewTransportClient_DefaultStdio(t *testing.T) {
	cfg := config.MCPServerConfig{Command: "some-cmd"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "stdio" {
		t.Errorf("empty transport with command should default to stdio, got %q", c.transportType)
	}
}

func TestNewTransportClient_DefaultHTTP(t *testing.T) {
	cfg := config.MCPServerConfig{URL: "http://example.com/mcp"}
	c := NewTransportClient("test", cfg)
	if c.transportType != "http" {
		t.Errorf("empty transport with url should default to http, got %q", c.transportType)
	}
}

func TestNewTransportClient_UnknownTransportFails(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "grpc", URL: "localhost:50051"}
	c := NewTransportClient("test", cfg)
	err := c.Start(context.Background())
	if err == nil {
		t.Error("expected error for unknown transport")
	}
}

func TestNewTransportClient_StdioFailsBadBinary(t *testing.T) {
	cfg := config.MCPServerConfig{Transport: "stdio", Command: "nonexistent-binary-xyz"}
	c := NewTransportClient("test", cfg)
	err := c.Start(context.Background())
	if err == nil {
		t.Error("expected error for bad binary")
	}
	if c.inner != nil {
		t.Error("inner should be nil after failed start")
	}
}

// ── InProcess client tests (transport-agnostic behavior) ─────────────────────

func TestClientToolsList(t *testing.T) {
	c := newTestClient(t)
	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["echo"] {
		t.Error("missing echo tool")
	}
	if !names["add"] {
		t.Error("missing add tool")
	}
}

func TestClientCallToolEcho(t *testing.T) {
	c := newTestClient(t)
	result, err := c.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := mcp.AsTextContent(result.Content[0]); ok {
		if text.Text != "hello" {
			t.Errorf("echo = %q, want hello", text.Text)
		}
	} else {
		t.Error("expected TextContent")
	}
}

func TestClientCallToolAdd(t *testing.T) {
	c := newTestClient(t)
	result, err := c.CallTool(context.Background(), "add", map[string]any{"a": float64(3), "b": float64(4)})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text, ok := mcp.AsTextContent(result.Content[0]); ok {
		if text.Text != "7" {
			t.Errorf("add = %q, want 7", text.Text)
		}
	} else {
		t.Error("expected TextContent")
	}
}

func TestClientStopNilsInner(t *testing.T) {
	c := newTestClient(t)
	if len(c.Tools()) == 0 {
		t.Fatal("expected tools")
	}
	c.Stop()
	if c.inner != nil {
		t.Error("inner should be nil after Stop")
	}
	if !c.closed {
		t.Error("closed should be true after Stop")
	}
}

// ── Pool tests ───────────────────────────────────────────────────────────────

func TestPoolHasTool(t *testing.T) {
	pool := NewPool()
	if pool.HasTool("nonexistent") {
		t.Error("empty pool should not have any tools")
	}
}

func TestPoolCallToolUnknown(t *testing.T) {
	pool := NewPool()
	_, err := pool.CallTool(context.Background(), "ghost", nil)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestMaxToolCallIterations(t *testing.T) {
	if MaxToolCallIterations != 10 {
		t.Errorf("MaxToolCallIterations = %d, want 10", MaxToolCallIterations)
	}
}

func TestPoolStopAllEmpty(t *testing.T) {
	pool := NewPool()
	pool.StopAll()
}

func TestPoolListToolsEmpty(t *testing.T) {
	pool := NewPool()
	tools := pool.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

// ── Config validation tests ──────────────────────────────────────────────────

func TestValidateMCPServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MCPServerConfig
		wantErr bool
	}{
		{"valid stdio", config.MCPServerConfig{Transport: "stdio", Command: "echo"}, false},
		{"valid http", config.MCPServerConfig{Transport: "http", URL: "http://localhost/mcp"}, false},
		{"valid sse", config.MCPServerConfig{Transport: "sse", URL: "http://localhost/sse"}, false},
		{"infer stdio", config.MCPServerConfig{Command: "echo"}, false},
		{"infer http", config.MCPServerConfig{URL: "http://localhost/mcp"}, false},
		{"stdio missing command", config.MCPServerConfig{Transport: "stdio"}, true},
		{"http missing url", config.MCPServerConfig{Transport: "http"}, true},
		{"unknown transport", config.MCPServerConfig{Transport: "grpc"}, true},
		{"empty config", config.MCPServerConfig{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := config.ValidateMCPServer("test", tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPoolRegisterFromConfig(t *testing.T) {
	pool := NewPool()
	servers := map[string]config.MCPServerConfig{
		"enabled":  {Transport: "stdio", Command: "echo"},
		"disabled": {Transport: "stdio", Command: "nope", Disabled: true},
	}
	pool.RegisterFromConfig(servers)
	if len(pool.clients) != 1 {
		t.Errorf("expected 1 client (disabled skipped), got %d", len(pool.clients))
	}
}
