package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func newBlockingSSEServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	backend := server.NewSSEServer(newMockServer())
	entered := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
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
			if req.Method == "tools/call" {
				close(entered)
				<-r.Context().Done()
				return
			}
		}
		backend.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	return srv, entered
}

func TestClientStopCancelsInFlightSSEPost(t *testing.T) {
	srv, entered := newBlockingSSEServer(t)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	done := make(chan error, 1)
	go func() { _, err := c.CallTool(ctx, "echo", nil); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("tool POST did not enter")
	}
	c.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped call error = %v, want context cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("Stop did not cancel in-flight SSE POST")
	}
}

func TestConfiguredSSEStartsAndCalls(t *testing.T) {
	backend := server.NewSSEServer(newMockServer())
	srv := httptest.NewServer(backend)
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start SSE: %v", err)
	}
	result, err := c.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("call SSE: %v", err)
	}
	if result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected SSE result: %+v", result)
	}
	text, ok := protocol.AsTextContent(result.Content[0])
	if !ok || text.Text != "hello" {
		t.Fatalf("unexpected SSE content: %+v", result.Content)
	}
}

func TestSSEStartupContextDoesNotOwnReadyStream(t *testing.T) {
	backend := server.NewSSEServer(newMockServer())
	srv := httptest.NewServer(backend)
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	testCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	startCtx, cancel := context.WithCancel(testCtx)
	if err := c.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	oldCtx, oldCancel := c.lifetime, c.cancelLifetime
	c.mu.Unlock()
	cancel()
	if _, err := c.CallTool(testCtx, "echo", nil); err != nil {
		t.Fatalf("startup cancellation closed ready stream: %v", err)
	}
	c.Stop()
	if oldCtx.Err() == nil {
		t.Fatal("old lifetime remained live")
	}
	if err := c.Start(testCtx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	oldCancel()
	if _, err := c.CallTool(testCtx, "echo", nil); err != nil {
		t.Fatalf("old generation canceled restarted stream: %v", err)
	}
}

func TestSSEFailedStartupCanRetry(t *testing.T) {
	backend := server.NewSSEServer(newMockServer())
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err == nil {
		t.Fatal("expected failed startup")
	}
	c.mu.Lock()
	inner, lifetime := c.inner, c.lifetime
	c.mu.Unlock()
	if inner != nil || lifetime != nil {
		t.Fatal("failed startup retained transport lifetime")
	}
	fail.Store(false)
	if err := c.Start(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := c.CallTool(ctx, "echo", nil); err != nil {
		t.Fatalf("retry call: %v", err)
	}
}

func TestSSECallerCancellationInterruptsPost(t *testing.T) {
	srv, entered := newBlockingSSEServer(t)
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	testCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	if err := c.Start(testCtx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.CallTool(ctx, "echo", nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("POST did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller cancel did not interrupt POST")
	}
}

func TestSSECancelDuringStartupAllowsRetry(t *testing.T) {
	backend := server.NewSSEServer(newMockServer())
	entered := make(chan struct{})
	var hold atomic.Bool
	hold.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && r.URL.Path == "/sse" {
			close(entered)
			<-r.Context().Done()
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	testCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE startup did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled startup succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup cancellation blocked")
	}
	c.mu.Lock()
	inner, lifetime := c.inner, c.lifetime
	c.mu.Unlock()
	if inner != nil || lifetime != nil {
		t.Fatal("canceled startup retained transport")
	}
	hold.Store(false)
	if err := c.Start(testCtx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := c.CallTool(testCtx, "echo", nil); err != nil {
		t.Fatalf("retry call: %v", err)
	}
}

func TestSSEStopDuringStartupAllowsRestart(t *testing.T) {
	backend := server.NewSSEServer(newMockServer())
	entered := make(chan struct{})
	var hold atomic.Bool
	hold.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && r.URL.Path == "/sse" {
			close(entered)
			<-r.Context().Done()
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("startup did not enter")
	}
	c.Stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped startup succeeded")
		}
	case <-ctx.Done():
		t.Fatal("Stop did not interrupt startup")
	}
	hold.Store(false)
	if err := c.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := c.CallTool(ctx, "echo", nil); err != nil {
		t.Fatalf("restart call: %v", err)
	}
}
