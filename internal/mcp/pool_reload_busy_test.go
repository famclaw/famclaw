package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
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
			p := NewPool("", false, false)
			cfg := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid"}
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
			changed, err := p.updateFromConfig(map[string]config.MCPServerConfig{"server": cfg}, nil, true)
			if err != nil || len(changed) != 1 || changed[0] != "server" {
				t.Fatalf("start was not scheduled for retry: %v %v", changed, err)
			}
		})
	}
}
