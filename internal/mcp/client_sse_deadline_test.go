package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
)

func TestSSEStartupPreservesDeadlineCause(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	c := NewTransportClient("fixture", config.MCPServerConfig{Transport: "sse", URL: srv.URL + "/sse"}, "", false, false)
	defer c.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("startup did not enter")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("start error = %v; want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not return")
	}
}
