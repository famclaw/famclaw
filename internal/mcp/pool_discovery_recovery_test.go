package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestPoolDiscoverySurvivesAmbiguousCall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		http500 bool
	}{
		{name: "lost response"},
		{name: "http 500 with private cause", http500: true},
	} {
		t.Run(tc.name, func(t *testing.T) { testPoolDiscoverySurvivesAmbiguousCall(t, tc.http500) })
	}
}

func testPoolDiscoverySurvivesAmbiguousCall(t *testing.T, http500 bool) {
	t.Helper()
	const privateCanary = "private-transport-error-canary"
	var mutations, reconciliations, otherCalls atomic.Int32
	s := server.NewMCPServer("primary", "1", server.WithToolCapabilities(true))
	s.AddTool(protocol.NewTool("mutate", protocol.WithDescription("write")), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		mutations.Add(1)
		return protocol.NewToolResultText("written"), nil
	})
	s.AddTool(protocol.NewTool("reconcile", protocol.WithDescription("read back"), protocol.WithString("key", protocol.Description("lookup key"))), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		reconciliations.Add(1)
		return protocol.NewToolResultText("observed"), nil
	})
	h := server.NewStreamableHTTPServer(s)
	var dropped, offline atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "tools/call" && req.Params.Name == "mutate" && dropped.CompareAndSwap(false, true) {
			recorder := httptest.NewRecorder()
			h.ServeHTTP(recorder, r) // execute the real handler, then lose its response
			if http500 {
				http.Error(w, privateCanary, http.StatusInternalServerError)
			} else {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer primary.Close()
	other := server.NewMCPServer("secondary", "1", server.WithToolCapabilities(true))
	other.AddTool(protocol.NewTool("other", protocol.WithDescription("unrelated")), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		otherCalls.Add(1)
		return protocol.NewToolResultText("ok"), nil
	})
	secondary := httptest.NewServer(server.NewStreamableHTTPServer(other))
	defer secondary.Close()
	p := NewPool("", false, false)
	p.RegisterFromConfig(map[string]config.MCPServerConfig{"primary": {Transport: "http", URL: primary.URL}, "secondary": {Transport: "http", URL: secondary.URL}}, nil)
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.StopAll()
	snapshot := func() ([]string, []ToolInfo) {
		names, infos := p.ListTools(), p.ListToolInfos()
		sort.Strings(names)
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
		return names, infos
	}
	names, infos := snapshot()
	if !reflect.DeepEqual(names, []string{"mutate", "other", "reconcile"}) {
		t.Fatalf("initial discovery: %v", names)
	}
	if len(infos) != 3 || infos[2].Description != "read back" || infos[2].ServerName != "primary" || len(infos[2].InputSchema) == 0 {
		t.Fatalf("initial metadata: %+v", infos)
	}
	result, err := p.CallTool(context.Background(), "mutate", nil)
	if err == nil || result != nil || mutations.Load() != 1 {
		t.Errorf("ambiguous call: result=%v err=%v mutation calls=%d", result, err, mutations.Load())
	}
	var callErr *toolCallError
	if http500 && (!errors.As(err, &callErr) || !strings.Contains(callErr.cause.Error(), privateCanary)) {
		t.Fatalf("test transport did not include private canary in the underlying cause: %v", err)
	}
	assertPrimary := func(wantStatus, wantError string) {
		t.Helper()
		for _, state := range p.ServerStates() {
			if state.Name == "primary" {
				if state.Status != wantStatus || state.Error != wantError || strings.Contains(state.Error, privateCanary) {
					t.Errorf("primary state = %+v, want status %q and safe error %q", state, wantStatus, wantError)
				}
				return
			}
		}
		t.Error("primary state missing")
	}
	assertPrimary("failed", "MCP connection failed after tool call")
	afterNames, afterInfos := snapshot()
	if !reflect.DeepEqual(names, afterNames) {
		t.Errorf("discovery lost after reset: before=%v after=%v", names, afterNames)
	}
	if !reflect.DeepEqual(infos, afterInfos) {
		t.Errorf("metadata lost after reset: before=%+v after=%+v", infos, afterInfos)
	}
	// A known reconciliation tool remains discoverable even while the
	// transport is down. Trying it cannot replay the ambiguous mutation.
	offline.Store(true)
	if !p.HasTool("reconcile") || p.HasTool("new_tool") {
		t.Fatal("unexpected alias availability while disconnected")
	}
	result, err = p.CallTool(context.Background(), "reconcile", map[string]any{"key": "x"})
	if err == nil || result != nil || mutations.Load() != 1 || reconciliations.Load() != 0 {
		t.Fatalf("offline reconciliation: result=%v err=%v mutation calls=%d reconciliation calls=%d", result, err, mutations.Load(), reconciliations.Load())
	}
	assertPrimary("failed", "MCP connection failed after tool call")
	// New metadata arrives on reconnect, but aliases are only rebuilt by
	// StartAll; a miss must not become a permanent negative entry.
	s.AddTool(protocol.NewTool("new_tool"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return protocol.NewToolResultText("new"), nil
	})
	offline.Store(false)
	result, err = p.CallTool(context.Background(), "reconcile", map[string]any{"key": "x"})
	if err != nil || result == nil || reconciliations.Load() != 1 || mutations.Load() != 1 {
		t.Errorf("distinct reconnect: result=%v err=%v reconciliation calls=%d mutation calls=%d", result, err, reconciliations.Load(), mutations.Load())
	}
	assertPrimary("started", "")
	p.clients["primary"].stateMu.Lock()
	staleError := p.clients["primary"].lastError
	p.clients["primary"].stateMu.Unlock()
	if staleError != nil {
		t.Errorf("successful reconnect retained stale error: %v", staleError)
	}
	if p.HasTool("new_tool") {
		t.Fatal("lazy reconnect unexpectedly rebuilt pool aliases")
	}
	// The refreshed client must not advertise a name without a route.
	reconnectedNames, reconnectedInfos := snapshot()
	if !reflect.DeepEqual(names, reconnectedNames) || !reflect.DeepEqual(infos, reconnectedInfos) {
		t.Errorf("unroutable tool advertised after lazy reconnect: names=%v infos=%+v", reconnectedNames, reconnectedInfos)
	}
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.HasTool("new_tool") {
		t.Fatal("new tool unavailable after pool alias rebuild")
	}
	result, err = p.CallTool(context.Background(), "new_tool", nil)
	if err != nil || result == nil {
		t.Fatalf("new tool after rebuild: result=%v err=%v", result, err)
	}
	result, err = p.CallTool(context.Background(), "other", nil)
	if err != nil || result == nil || otherCalls.Load() != 1 {
		t.Errorf("unrelated success: result=%v err=%v calls=%d", result, err, otherCalls.Load())
	}
	finalNames, finalInfos := snapshot()
	if !reflect.DeepEqual(finalNames, []string{"mutate", "new_tool", "other", "reconcile"}) || len(finalInfos) != 4 || finalInfos[1].Name != "new_tool" {
		t.Errorf("unexpected discovery after refresh: names=%v infos=%+v", finalNames, finalInfos)
	}
}
