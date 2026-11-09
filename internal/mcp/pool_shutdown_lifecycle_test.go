package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	protocol "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestMCPBlockedStdioHelper speaks just enough MCP to reach tools/list, then
// deliberately stops reading stdin so a large tool request fills its pipe.
func TestMCPBlockedStdioHelper(t *testing.T) {
	if os.Getenv("FAMCLAW_MCP_BLOCK_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "initialize":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":%q,\"capabilities\":{\"tools\":{}},\"serverInfo\":{\"name\":\"test\",\"version\":\"1\"}}}\n", req.ID, req.Params.ProtocolVersion)
		case "tools/list":
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"large\",\"inputSchema\":{\"type\":\"object\"}}]}}\n", req.ID)
			buf := make([]byte, 256)
			if _, err := reader.Read(buf); err != nil {
				os.Exit(3)
			}
			if err := os.WriteFile(os.Getenv("FAMCLAW_MCP_READY_FILE"), []byte("ready"), 0600); err != nil {
				os.Exit(4)
			}
			time.Sleep(10 * time.Second)
			os.Exit(0)
		}
	}
}

func TestPoolStopAllInterruptsBlockedStdio(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cfg := config.MCPServerConfig{Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestMCPBlockedStdioHelper$"}, Env: map[string]string{"FAMCLAW_MCP_BLOCK_HELPER": "1", "FAMCLAW_MCP_READY_FILE": ready}}
	p := NewPool("", false, false)
	p.RegisterFromConfig(map[string]config.MCPServerConfig{"stall": cfg}, nil)
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { p.StopAll(); close(done) }()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Error("test helper cleanup did not finish")
		}
	})
	if !p.HasTool("large") {
		t.Fatal("test server did not initialize")
	}
	started := make(chan struct{})
	called := make(chan error, 1)
	go func() {
		close(started)
		_, err := p.CallTool(context.Background(), "large", map[string]any{"payload": strings.Repeat("x", 8<<20)})
		called <- err
	}()
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-called:
			t.Fatalf("call finished before child read request prefix: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not read request prefix")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-called:
		t.Fatalf("call finished although child stopped reading: %v", err)
	default:
	}
	// A concurrent StartAll must not wait on the in-flight call's mc.mu
	// while holding the pool lock that shutdown needs.
	startDone := make(chan error, 1)
	go func() { startDone <- p.StartAll(context.Background()) }()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartAll waited on the blocked call")
	}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not close blocked stdio")
	}
	select {
	case err := <-called:
		if err == nil {
			t.Fatal("blocked call reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked write did not return")
	}
}

func TestClientConcurrentStartWaiterHonorsContext(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); srv.Close() }()
	c := NewTransportClient("slow", config.MCPServerConfig{Transport: "http", URL: srv.URL}, "", false, false)
	first := make(chan error, 1)
	go func() { first <- c.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initialize did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := c.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter err=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("waiter did not honor deadline")
	}
	c.Stop()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("stopped initialize did not return")
	}
}

func TestPoolRestartAndStaleLookup(t *testing.T) {
	var calls atomic.Int32
	server := mcpserver.NewMCPServer("test", "1", mcpserver.WithToolCapabilities(true))
	server.AddTool(protocol.NewTool("echo"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		calls.Add(1)
		return protocol.NewToolResultText("ok"), nil
	})
	httpServer := httptest.NewServer(mcpserver.NewStreamableHTTPServer(server))
	defer httpServer.Close()
	cfg := config.MCPServerConfig{Transport: "http", URL: httpServer.URL}
	p := NewPool("", false, false)
	p.RegisterFromConfig(map[string]config.MCPServerConfig{"test": cfg}, nil)
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	mc := p.clients["echo"]
	mc.stateMu.Lock()
	oldGeneration := mc.generation
	mc.stateMu.Unlock()
	p.StopAll()
	// A fresh call keeps the pre-existing lazy-restart behavior.
	if _, err := p.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatalf("lazy restart: %v", err)
	}
	p.StopAll()
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.callManaged(context.Background(), "echo", nil, mc, oldGeneration); !errors.Is(err, errClientStopped) {
		t.Fatalf("old lookup was not fenced: %v", err)
	}
	if _, err := p.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatalf("explicit restart: %v", err)
	}
	p.StopAll()
	if got := calls.Load(); got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}
	if mc.snapshotClient().started() {
		t.Fatal("second StopAll left a started client")
	}
	mc.stateMu.Lock()
	beforeRemoval := mc.generation
	mc.stateMu.Unlock()
	p.UpdateFromConfig(map[string]config.MCPServerConfig{}, nil)
	if _, err := p.callManaged(context.Background(), "echo", nil, mc, beforeRemoval); !errors.Is(err, errClientStopped) {
		t.Fatalf("lookup captured before config removal was not fenced: %v", err)
	}
}

func TestClientConcurrentStartAndDirectRestart(t *testing.T) {
	var initializations atomic.Int32
	server := mcpserver.NewMCPServer("test", "1", mcpserver.WithToolCapabilities(true))
	server.AddTool(protocol.NewTool("echo"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return protocol.NewToolResultText("ok"), nil
	})
	handler := mcpserver.NewStreamableHTTPServer(server)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "initialize" || request.Method == "server/discover" {
			initializations.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	c := NewTransportClient("test", config.MCPServerConfig{Transport: "http", URL: httpServer.URL}, "", false, false)
	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() { errs <- c.Start(context.Background()) }()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent start: %v", err)
		}
	}
	if initializations.Load() != 1 {
		t.Fatalf("initializations = %d, want 1", initializations.Load())
	}
	c.Stop()
	if _, err := c.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatalf("direct lazy restart: %v", err)
	}
	c.Stop()
	if c.started() {
		t.Fatal("second stop left transport attached")
	}
}

func TestStopAllInterruptsConcurrentStartAll(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); srv.Close() }()
	p := NewPool("", false, false)
	p.RegisterFromConfig(map[string]config.MCPServerConfig{"slow": {Transport: "http", URL: srv.URL}}, nil)
	started := make(chan error, 1)
	go func() { started <- p.StartAll(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup handshake did not begin")
	}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("StopAll waited for the blocked handshake")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stopped handshake did not return")
	}
}
