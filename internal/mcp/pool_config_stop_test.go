package mcp

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
)

type configBlockedCall struct {
	mcpclient.MCPClient
	entered, released, cancelled chan struct{}
	calls                        atomic.Int32
}

func (c *configBlockedCall) CallTool(ctx context.Context, _ protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	c.calls.Add(1)
	close(c.entered)
	select {
	case <-ctx.Done():
		close(c.cancelled)
		return nil, ctx.Err()
	case <-c.released:
		return protocol.NewToolResultText("done"), nil
	}
}
func (c *configBlockedCall) Close() error { return nil }

type configImmediateCall struct{ mcpclient.MCPClient }

func (configImmediateCall) CallTool(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	return protocol.NewToolResultText("ok"), nil
}
func (configImmediateCall) Close() error { return nil }

func waitConfigReservation(t *testing.T, life *serverLifecycle) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		life.mu.Lock()
		reserved := life.closing != nil
		life.mu.Unlock()
		if reserved {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement did not reserve lifecycle")
		}
		runtime.Gosched()
	}
}

func TestConfigReplacementWaitAllowsShutdownAndOtherServer(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary update", true: "shutdown"}[stop], func(t *testing.T) {
			inner := &configBlockedCall{entered: make(chan struct{}), released: make(chan struct{}), cancelled: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(inner.released) }) })
			p := NewPool("", false, false)
			old := config.MCPServerConfig{Transport: "http", URL: "http://old.invalid"}
			other := config.MCPServerConfig{Transport: "http", URL: "http://other.invalid"}
			p.clients["server"] = &managedClient{name: "server", cfg: old, client: &Client{inner: inner, ready: true}}
			p.clients["other"] = &managedClient{name: "other", cfg: other, client: &Client{inner: configImmediateCall{}, ready: true}}
			life := p.lifecycle("server")
			p.lifecycle("other")
			called := make(chan error, 1)
			go func() { _, err := p.CallTool(context.Background(), "server", nil); called <- err }()
			select {
			case <-inner.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("call did not enter")
			}
			updated := make(chan struct{})
			go func() {
				p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: "http://new.invalid"}, "other": other}, nil)
				close(updated)
			}()
			waitConfigReservation(t, life)
			otherDone := make(chan error, 1)
			go func() { _, err := p.CallTool(context.Background(), "other", nil); otherDone <- err }()
			select {
			case err := <-otherDone:
				if err != nil {
					t.Fatalf("unrelated call: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("unrelated server blocked by replacement")
			}
			select {
			case <-updated:
				t.Fatal("replacement completed before active call")
			default:
			}
			if stop {
				stopped := make(chan struct{})
				go func() { p.StopAll(); close(stopped) }()
				select {
				case <-inner.cancelled:
				case <-time.After(2 * time.Second):
					t.Error("shutdown did not cancel active call")
				}
				release.Do(func() { close(inner.released) })
				select {
				case <-stopped:
				case <-time.After(2 * time.Second):
					t.Error("shutdown did not finish")
				}
			} else {
				select {
				case <-inner.cancelled:
					t.Error("ordinary update cancelled active call")
				default:
				}
				release.Do(func() { close(inner.released) })
			}
			select {
			case err := <-called:
				if stop && !errors.Is(err, context.Canceled) {
					t.Errorf("stopped call error = %v, want cancellation", err)
				}
				if !stop && err != nil {
					t.Errorf("ordinary update interrupted call: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Error("call did not finish")
			}
			select {
			case <-updated:
			case <-time.After(2 * time.Second):
				t.Error("replacement did not finish")
			}
			if got := inner.calls.Load(); got != 1 {
				t.Errorf("call count = %d, want 1", got)
			}
		})
	}
}

func TestTryLockManagedClientsReleasesPartialAcquisition(t *testing.T) {
	first, busy := &managedClient{}, &managedClient{}
	busy.mu.Lock()
	locked, gotBusy := tryLockManagedClients([]*managedClient{first, busy})
	busy.mu.Unlock()
	if gotBusy != busy || len(locked) != 0 {
		t.Fatalf("got locked=%d busy=%p, want no locks and %p", len(locked), gotBusy, busy)
	}
	if !first.mu.TryLock() {
		t.Fatal("partial acquisition was retained after contention")
	}
	first.mu.Unlock()
}

func TestConcurrentConfigUpdatesAndStopDoNotDeadlock(t *testing.T) {
	inner := &configBlockedCall{entered: make(chan struct{}), released: make(chan struct{}), cancelled: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(inner.released) }) })
	p := NewPool("", false, false)
	old := config.MCPServerConfig{Transport: "http", URL: "http://old.invalid"}
	p.clients["server"] = &managedClient{name: "server", cfg: old, client: &Client{inner: inner, ready: true}}
	life := p.lifecycle("server")
	called := make(chan struct{})
	go func() { _, _ = p.CallTool(context.Background(), "server", nil); close(called) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not enter")
	}
	update := func(url string) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: url}}, nil)
			close(done)
		}()
		return done
	}
	first := update("http://first.invalid")
	waitConfigReservation(t, life)
	second := update("http://second.invalid")
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-inner.cancelled:
	case <-time.After(2 * time.Second):
		t.Error("shutdown did not cancel active call")
	}
	release.Do(func() { close(inner.released) })
	for name, done := range map[string]<-chan struct{}{"call": called, "first update": first, "second update": second, "shutdown": stopped} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("%s did not finish", name)
		}
	}
}
