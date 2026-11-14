package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/famclaw/famclaw/internal/config"
)

type panicListTransport struct {
	base  http.RoundTripper
	owner *Client
	saved mcpclient.MCPClient
	fired bool
}

func (p *panicListTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Method == "tools/list" && !p.fired {
			p.fired = true
			p.owner.mu.Lock()
			p.saved = p.owner.inner
			p.owner.mu.Unlock()
			panic("test discovery panic")
		}
	}
	return p.base.RoundTrip(r)
}

func TestStartPanicClosesOwnedTransportAndAllowsRetry(t *testing.T) {
	if os.Getenv("FAMCLAW_UNWIND_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStartPanicClosesOwnedTransportAndAllowsRetry$")
		cmd.Env = append(os.Environ(), "FAMCLAW_UNWIND_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated child: %v\n%s", err, out)
		}
		return
	}
	backend := server.NewMCPServer("fixture", "1", server.WithToolCapabilities(true))
	backend.AddTool(protocol.NewTool("echo"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return protocol.NewToolResultText("ok"), nil
	})
	srv := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "http", URL: srv.URL}, "", false, false)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	hook := &panicListTransport{base: originalTransport, owner: c}
	http.DefaultTransport = hook
	var recovered any
	func() { defer func() { recovered = recover() }(); _ = c.Start(context.Background()) }()
	if recovered != "test discovery panic" {
		t.Fatalf("panic not propagated: %v", recovered)
	}
	if !hook.fired || hook.saved == nil {
		t.Fatal("panic did not occur after transport publication")
	}
	c.mu.Lock()
	remaining := c.inner
	c.mu.Unlock()
	if remaining != nil {
		t.Fatal("panicking startup left its transport attached")
	}
	if _, err := hook.saved.ListTools(context.Background(), protocol.ListToolsRequest{}); err == nil {
		t.Fatal("panicking startup did not close its transport")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("explicit retry failed: %v", err)
	}
	c.Stop()
}

type closeSpy struct {
	mcpclient.MCPClient
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	err     error
}

func (s *closeSpy) Close() error {
	s.calls.Add(1)
	if s.entered != nil {
		close(s.entered)
	}
	if s.release != nil {
		<-s.release
	}
	return s.err
}

func TestInterruptedStartupStopKeepsSingleCloseOwner(t *testing.T) {
	inner := &closeSpy{entered: make(chan struct{}), release: make(chan struct{})}
	c := &Client{inner: inner}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(inner.release) }) })
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-inner.entered:
	case <-time.After(time.Second):
		t.Fatal("Stop did not close transport")
	}
	c.closeUnready(inner)
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("transport closed %d times", got)
	}
	once.Do(func() { close(inner.release) })
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete")
	}
}

func TestPanicCleanupHoldsStartFenceAndPreservesPanic(t *testing.T) {
	inner := &closeSpy{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic close error")}
	c := &Client{inner: inner}
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	p.mu.Lock()
	life := p.lifecycle("server")
	p.mu.Unlock()
	if err := life.begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(inner.release) }) })
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		defer life.finishStart()
		defer c.closeUnready(inner)
		panic("original panic")
	}()
	select {
	case <-inner.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not close transport")
	}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	deadline := time.Now().Add(time.Second)
	for {
		life.mu.Lock()
		closing := life.closing != nil
		life.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not establish fence")
		}
		runtime.Gosched()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.CallTool(ctx, "server", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start passed blocked cleanup: %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("stop returned before cleanup completed")
	default:
	}
	once.Do(func() { close(inner.release) })
	select {
	case v := <-recovered:
		if v != "original panic" {
			t.Fatalf("panic changed: %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("panic cleanup did not return")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not complete")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("transport closed %d times", got)
	}
}

func TestPoolCallStartupPanicUnwindsAdmission(t *testing.T) {
	if os.Getenv("FAMCLAW_POOL_UNWIND_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPoolCallStartupPanicUnwindsAdmission$")
		cmd.Env = append(os.Environ(), "FAMCLAW_POOL_UNWIND_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated child: %v\n%s", err, out)
		}
		return
	}
	backend := server.NewMCPServer("fixture", "1", server.WithToolCapabilities(true))
	backend.AddTool(protocol.NewTool("echo"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return protocol.NewToolResultText("ok"), nil
	})
	srv := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	defer srv.Close()
	cfg := config.MCPServerConfig{Transport: "http", URL: srv.URL}
	c := NewTransportClient("fixture", cfg, "", false, false)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	hook := &panicListTransport{base: originalTransport, owner: c}
	http.DefaultTransport = hook
	p := NewPool("", false, false)
	p.clients["fixture"] = &managedClient{name: "fixture", cfg: cfg, client: c}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = p.CallTool(context.Background(), "fixture", nil)
	}()
	if recovered != "test discovery panic" {
		t.Fatalf("panic not propagated: %v", recovered)
	}
	done := make(chan struct{})
	go func() { p.StopAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic leaked the startup admission")
	}
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatalf("explicit restart: %v", err)
	}
	if !p.HasTool("echo") {
		t.Fatal("explicit restart did not rediscover tool")
	}
	if _, err := p.CallTool(context.Background(), "echo", nil); err != nil {
		t.Fatalf("new call after restart: %v", err)
	}
	p.StopAll()
}

func TestListToolsErrorKeepsPreviouslyCachedTools(t *testing.T) {
	backend := server.NewMCPServer("fixture", "1", server.WithToolCapabilities(true))
	handler := server.NewStreamableHTTPServer(backend)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var req struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(b, &req)
			if req.Method == "tools/list" {
				http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "http", URL: srv.URL}, "", false, false)
	c.tools = []protocol.Tool{protocol.NewTool("retained")}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("ListTools error changed startup contract: %v", err)
	}
	if !c.started() || len(c.Tools()) != 1 || c.Tools()[0].Name != "retained" {
		t.Fatal("cached tools were not retained")
	}
	c.Stop()
}
