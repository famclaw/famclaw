package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// The first tool request commits a synthetic mutation then loses its reply.
// Any reissued request reaches the MCP handler and succeeds instead.
func TestPoolLostReplyDoesNotReinvoke(t *testing.T) {
	var calls atomic.Int32
	s := server.NewMCPServer("fake", "1", server.WithToolCapabilities(true))
	s.AddTool(protocol.NewTool("mutate"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		calls.Add(1)
		return protocol.NewToolResultText("ok"), nil
	})
	handler := server.NewStreamableHTTPServer(s)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("read request")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "tools/call" && calls.CompareAndSwap(0, 1) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error("hijack failed")
				return
			}
			_ = conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	cfg := config.MCPServerConfig{Transport: "http", URL: httpServer.URL}
	p := NewPool("", false, false)
	p.clients["mutate"] = &managedClient{name: "fake", cfg: cfg, client: NewTransportClient("fake", cfg, "", false, false)}
	t.Cleanup(func() { p.clients["mutate"].client.Stop() })
	result, err := p.CallTool(context.Background(), "mutate", nil)
	if got := calls.Load(); got != 1 {
		t.Errorf("mutation invocations = %d, want 1", got)
	}
	if err == nil || result != nil {
		t.Errorf("lost reply must return an uncertain error, not a result")
	}
}

func TestPoolFirstSuccessControl(t *testing.T) {
	c := newTestClient(t)
	p := NewPool("", false, false)
	p.clients["echo"] = &managedClient{name: "mock", client: c}
	result, err := p.CallTool(context.Background(), "echo", map[string]any{"text": "ok"})
	if err != nil || result == nil || len(result.Content) != 1 {
		t.Fatalf("successful call failed")
	}
	text, ok := result.Content[0].(protocol.TextContent)
	if !ok || text.Text != "ok" {
		t.Fatal("successful text was not preserved")
	}
}

type failingInner struct {
	mcpclient.MCPClient
	cause error
	calls int
}

func (f *failingInner) CallTool(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	f.calls++
	return nil, f.cause
}

func TestPoolFirstCauseAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"transport", errors.New("synthetic first cause")},
		{"canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t)
			fake := &failingInner{MCPClient: c.inner, cause: tc.cause}
			c.inner = fake
			p := NewPool("", false, false)
			p.clients["echo"] = &managedClient{name: "mock", cfg: config.MCPServerConfig{Transport: "invalid"}, client: c}
			_, err := p.CallTool(context.Background(), "echo", nil)
			if fake.calls != 1 {
				t.Errorf("calls = %d, want 1", fake.calls)
			}
			if !errors.Is(err, tc.cause) {
				t.Error("first error identity was lost")
			}
			if err != nil && tc.name == "transport" && strings.Contains(err.Error(), tc.cause.Error()) {
				t.Error("private cause was exposed in error text")
			}
		})
	}
}
