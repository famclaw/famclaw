package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestReloadReportsPriorCloseBusy(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	p.clients["other"] = &managedClient{name: "other", client: newTestClient(t)}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(inner.release) }) })
	stopDone := make(chan struct{})
	go func() { p.StopAll(); close(stopDone) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not start")
	}
	reloadDone := make(chan map[string]error, 1)
	go func() { reloadDone <- p.ReloadConfig(&config.Config{}) }()
	reloadFinished := false
	select {
	case result := <-reloadDone:
		reloadFinished = true
		if !errors.Is(result[""], ErrReloadBusy) {
			t.Error("busy reload was reported successful")
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("ReloadConfig did not return while prior close remained blocked")
	}
	lookup := make(chan bool, 1)
	go func() { lookup <- p.HasTool("other") }()
	select {
	case ok := <-lookup:
		if !ok {
			t.Error("unrelated server lookup was lost")
		}
	case <-time.After(time.Second):
		t.Error("unrelated server lookup blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.CallTool(ctx, "server", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("fenced call cancellation: %v", err)
	}
	// Always unblock and join both goroutines; this test must not leave live work.
	releaseOnce.Do(func() { close(inner.release) })
	for name, done := range map[string]<-chan struct{}{"stop": stopDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("%s did not finish after release", name)
		}
	}
	if !reloadFinished {
		select {
		case <-reloadDone:
		case <-time.After(2 * time.Second):
			t.Error("reload did not finish after release")
		}
	}
	if result := p.ReloadConfig(&config.Config{}); len(result) != 0 {
		t.Errorf("retry after close: %v", result)
	}
}

func TestReloadBusyLocksLeaveReservationsUsable(t *testing.T) {
	for _, lock := range []string{"reload", "config", "client"} {
		t.Run(lock, func(t *testing.T) {
			p := NewPool("", false, false)
			mc := &managedClient{name: "server", client: newTestClient(t)}
			p.clients["server"] = mc
			life := p.lifecycle("server")
			switch lock {
			case "reload":
				p.reloadMu.Lock()
			case "config":
				p.configMu.Lock()
			case "client":
				mc.mu.Lock()
			}
			result := p.ReloadConfig(&config.Config{})
			switch lock {
			case "reload":
				p.reloadMu.Unlock()
			case "config":
				p.configMu.Unlock()
			case "client":
				mc.mu.Unlock()
			}
			if !errors.Is(result[""], ErrReloadBusy) {
				t.Fatalf("missing busy error: %v", result)
			}
			life.mu.Lock()
			closing, closes := life.closing, life.closes
			life.mu.Unlock()
			if closing != nil || closes != 0 {
				t.Fatal("owned reservation was not released")
			}
			if result := p.ReloadConfig(&config.Config{}); len(result) != 0 {
				t.Fatalf("retry failed: %v", result)
			}
		})
	}
}

func TestReloadBusyStartDoesNotStrandOtherReservations(t *testing.T) {
	p := NewPool("", false, false)
	for _, name := range []string{"active", "other"} {
		p.clients[name] = &managedClient{name: name, client: newTestClient(t)}
		p.lifecycle(name)
	}
	active, other := p.lifecycles["active"], p.lifecycles["other"]
	active.starts = 1
	result := p.ReloadConfig(&config.Config{})
	if !errors.Is(result[""], ErrReloadBusy) {
		t.Fatalf("missing busy error: %v", result)
	}
	if active.closing != nil || active.starts != 1 || other.closing != nil || other.closes != 0 {
		t.Fatal("busy start left a spurious lifecycle fence")
	}
	active.finishStart()
	if result := p.ReloadConfig(&config.Config{}); len(result) != 0 {
		t.Fatalf("retry after start: %v", result)
	}
}

func TestReloadAdapterReportsGlobalBusy(t *testing.T) {
	p := NewPool("", false, false)
	p.configMu.Lock()
	err := (ReloadReloader{Pool: p}).ReloadConfig(&config.Config{})
	p.configMu.Unlock()
	if !errors.Is(err, ErrReloadBusy) {
		t.Fatalf("reload adapter lost busy error: %v", err)
	}
}

func TestReloadRetriesStartPreviouslyBlockedByClientLock(t *testing.T) {
	for _, reason := range []string{"lock", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			var calls atomic.Int32
			s := server.NewMCPServer("retry", "1", server.WithToolCapabilities(true))
			s.AddTool(protocol.NewTool("retry_probe"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
				calls.Add(1)
				return protocol.NewToolResultText("ok"), nil
			})
			endpoint := httptest.NewServer(server.NewStreamableHTTPServer(s))
			defer endpoint.Close()
			p := NewPool("", false, false)
			defer p.StopAll()
			cfg := config.MCPServerConfig{Transport: "http", URL: endpoint.URL}
			mc := &managedClient{name: "server", cfg: cfg, client: NewTransportClient("server", cfg, "", false, false)}
			p.clients["server"] = mc
			life := p.lifecycle("server")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(ErrReloadBusy)
			if reason == "lock" {
				mc.mu.Lock()
			} else {
				cancel()
				want = context.Canceled
			}
			err := startReloadClient(ctx, mc, 0, life)
			if reason == "lock" {
				mc.mu.Unlock()
			}
			if !errors.Is(err, want) || life.starts != 0 {
				t.Fatalf("start result %v; starts=%d", err, life.starts)
			}
			retryConfig := &config.Config{Skills: config.SkillsConfig{MCPServers: map[string]config.MCPServerConfig{"server": cfg}}}
			results := p.ReloadConfig(retryConfig)
			if result, ok := results["server"]; !ok || result != nil {
				t.Fatalf("retry did not start server: %v", results)
			}
			if !p.HasTool("retry_probe") {
				t.Fatal("retry did not expose discovered tool")
			}
			if _, err := p.CallTool(context.Background(), "retry_probe", nil); err != nil || calls.Load() != 1 {
				t.Fatalf("retried tool call: err=%v calls=%d", err, calls.Load())
			}
			mc.stateMu.Lock()
			pending := mc.reloadStartPending
			mc.stateMu.Unlock()
			life.mu.Lock()
			starts, closes, closing := life.starts, life.closes, life.closing
			life.mu.Unlock()
			if pending || starts != 0 || closes != 0 || closing != nil {
				t.Fatal("retry did not clear pending state and balance lifecycle")
			}
		})
	}
}
