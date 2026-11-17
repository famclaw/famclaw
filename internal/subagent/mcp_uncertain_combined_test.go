package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubagentMCPUncertainCauseMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want string
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(context.Background())
			cancel()
			return c, func() {}
		}, "cancelled"},
		{"timeout", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := server.NewMCPServer("test", "1")
			srv.AddTool(protocol.NewTool("operation"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
				return protocol.NewToolResultText("ok"), nil
			})
			pool := mcp.NewPool("", false, false)
			if err := pool.RegisterInProcessServer("test", srv); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.StopAll)
			ctx, cancel := tc.ctx()
			defer cancel()
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}
			got, ok := dispatchSubagentTool(ctx, call, ExecutorDeps{Pool: pool}, map[string]bool{"operation": true}, nil, nil, time.Now())
			if !ok || !strings.Contains(got, "outcome uncertain") || !strings.Contains(got, "Verify before trying again") || !strings.Contains(got, "Error class: "+tc.want+".") {
				t.Fatalf("dispatch=(%q,%v)", got, ok)
			}
		})
	}
}

// Both a lost response after execution and a nil server handler result are
// reported as protocol/transport errors by the concrete MCP client. Neither
// establishes whether the tool's effects occurred.
func TestSubagentMCPUncertainReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		readonly, annotated, nilResult bool
		want                           string
	}{
		{"readonly lost response", true, true, false, "Tool outcome uncertain. It is marked read-only; its effects are not verified. Verify before trying again."},
		{"mutating lost response", false, true, false, "Tool outcome uncertain; some changes may have occurred. Verify before trying again."},
		{"unannotated lost response", false, false, false, "Tool outcome uncertain; some changes may have occurred. Verify before trying again."},
		{"readonly nil server result", true, true, true, "Tool outcome uncertain. It is marked read-only; its effects are not verified. Verify before trying again."},
		{"mutating nil server result", false, true, true, "Tool outcome uncertain; some changes may have occurred. Verify before trying again."},
		{"unannotated nil server result", false, false, true, "Tool outcome uncertain; some changes may have occurred. Verify before trying again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := server.NewMCPServer("fake", "1", server.WithToolCapabilities(true))
			options := []protocol.ToolOption{}
			if tc.annotated {
				options = append(options, protocol.WithReadOnlyHintAnnotation(tc.readonly))
			}
			srv.AddTool(protocol.NewTool("operation", options...), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
				calls.Add(1)
				if tc.nilResult {
					return nil, nil
				}
				return protocol.NewToolResultText("fake-result"), nil
			})
			handler := server.NewStreamableHTTPServer(srv)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				var req struct {
					Method string `json:"method"`
				}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
					return
				}
				if req.Method == "tools/call" && !tc.nilResult {
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			pool := mcp.NewPool("", false, false)
			pool.RegisterFromConfig(map[string]config.MCPServerConfig{"fake": {Transport: "http", URL: endpoint.URL}}, nil)
			if err := pool.StartAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer pool.StopAll()
			_, allowed, readonly := filterTools(pool.ListToolInfos(), []string{"operation"}, nil)
			if readonly["operation"] != tc.readonly {
				t.Fatalf("readonly metadata=%v want=%v", readonly["operation"], tc.readonly)
			}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "operation", Arguments: map[string]any{}}}
			out, ok := dispatchSubagentTool(context.Background(), call, ExecutorDeps{Pool: pool}, allowed, nil, readonly, time.Now())
			if !ok || out != tc.want {
				t.Fatalf("dispatch=(%q,%v) want=(%q,true)", out, ok, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("tool handler calls=%d want 1; no automatic replay", calls.Load())
			}
			if strings.Contains(out, "fake-result") || strings.Contains(out, "failed") || strings.Contains(out, "no changes") {
				t.Fatalf("misleading or private diagnostic: %q", out)
			}
		})
	}
}
