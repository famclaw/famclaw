package mcp

import (
	"context"
	"fmt"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
		cmd:   "mock",
		inner: inner,
		tools: toolsResult.Tools,
	}
}

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

// TestClientLifecycle exercises the real Client Start/Stop/reconnect path.
func TestClientLifecycle(t *testing.T) {
	c := NewClient("nonexistent-binary-that-should-fail")

	// Start should fail with a bad binary
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected error starting with bad binary")
	}

	// inner should still be nil after failed start
	if c.inner != nil {
		t.Error("inner should be nil after failed start")
	}

	// Tools should be empty
	if len(c.Tools()) != 0 {
		t.Error("tools should be empty after failed start")
	}

	// Stop should not panic on never-started client
	c.Stop()
}

// TestClientStopAndReconnect verifies Stop nils inner to allow reconnect.
func TestClientStopAndReconnect(t *testing.T) {
	c := newTestClient(t)

	// Should have tools
	if len(c.Tools()) == 0 {
		t.Fatal("expected tools")
	}

	// Stop
	c.Stop()

	// inner should be nil after stop
	if c.inner != nil {
		t.Error("inner should be nil after Stop")
	}
	if !c.closed {
		t.Error("closed should be true after Stop")
	}
}
