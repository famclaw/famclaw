package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/famclaw/famclaw/internal/config"
	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type gatedConfigClose struct {
	mcpclient.MCPClient
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	err     error
}

func (c *gatedConfigClose) Close() error {
	close(c.entered)
	<-c.release
	close(c.done)
	return c.err
}

func TestConfigRetireDoesNotHoldPoolReadersDuringClose(t *testing.T) {
	for _, tc := range []struct {
		name    string
		servers map[string]config.MCPServerConfig
	}{
		{name: "remove", servers: nil},
		{name: "disable", servers: map[string]config.MCPServerConfig{"server": {Disabled: true}}},
		{name: "replace", servers: map[string]config.MCPServerConfig{"server": {Transport: "http", URL: "http://example.invalid/new"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &gatedConfigClose{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(inner.release) }) })
			p := NewPool("", false, false)
			cfg := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid/old"}
			p.clients["server"] = &managedClient{name: "server", cfg: cfg, client: &Client{inner: inner, ready: true}}
			p.lifecycle("server")
			updated := make(chan struct{})
			go func() { p.UpdateFromConfig(tc.servers, nil); close(updated) }()
			select {
			case <-inner.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("retired transport did not enter Close")
			}
			readers := make(chan struct{})
			go func() { p.HasTool("server"); p.ListTools(); p.ServerStates(); close(readers) }()
			select {
			case <-readers:
			case <-time.After(time.Second):
				t.Fatal("pool reader blocked by retired transport Close")
			}
			select {
			case <-inner.done:
				t.Fatal("transport closed before release")
			default:
			}
			release.Do(func() { close(inner.release) })
			select {
			case <-updated:
			case <-time.After(2 * time.Second):
				t.Fatal("config update did not finish")
			}
		})
	}
}

func TestConfigReplacementFencesInitializeUntilCloseFinishes(t *testing.T) {
	for _, closeError := range []bool{false, true} {
		t.Run(map[bool]string{false: "close succeeds", true: "close fails"}[closeError], func(t *testing.T) {
			var initializes atomic.Int32
			backend := mcpserver.NewMCPServer("test", "1", mcpserver.WithToolCapabilities(true))
			handler := mcpserver.NewStreamableHTTPServer(backend)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read request", http.StatusBadRequest)
					return
				}
				var req struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &req)
				if req.Method == "initialize" || req.Method == "server/discover" {
					initializes.Add(1)
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			inner := &gatedConfigClose{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			if closeError {
				inner.err = errors.New("synthetic close error")
			}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(inner.release) }) })
			p := NewPool("", false, false)
			old := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid/old"}
			p.clients["server"] = &managedClient{name: "server", cfg: old, client: &Client{inner: inner, ready: true}}
			p.lifecycle("server")
			updated := make(chan struct{})
			go func() {
				p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: srv.URL}}, nil)
				close(updated)
			}()
			select {
			case <-inner.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("retired transport did not enter Close")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			started := make(chan struct{})
			go func() { _ = p.StartAll(ctx); close(started) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("fenced startup did not honor context")
			}
			if got := initializes.Load(); got != 0 {
				t.Fatalf("replacement initialized before old Close finished: %d", got)
			}
			release.Do(func() { close(inner.release) })
			select {
			case <-inner.done:
			case <-time.After(2 * time.Second):
				t.Fatal("retired Close did not finish")
			}
			select {
			case <-updated:
			case <-time.After(2 * time.Second):
				t.Fatal("config update did not finish")
			}
			startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer startCancel()
			if err := p.StartAll(startCtx); err != nil {
				t.Fatal(err)
			}
			if got := initializes.Load(); got == 0 {
				t.Fatal("replacement never initialized after Close returned")
			}
			p.StopAll()
		})
	}
}

func TestConfigReAddWaitsForRetiredTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &gatedConfigClose{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
		var release sync.Once
		defer release.Do(func() { close(inner.release) })
		p := NewPool("", false, false)
		cfg := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid/server"}
		p.clients["server"] = &managedClient{name: "server", cfg: cfg, client: &Client{inner: inner, ready: true}}
		p.lifecycle("server")
		p.UpdateFromConfig(nil, nil)
		<-inner.entered
		added := make(chan struct{})
		go func() {
			p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": cfg}, nil)
			close(added)
		}()
		synctest.Wait()
		select {
		case <-added:
			t.Fatal("server re-added before retired transport closed")
		default:
		}
		release.Do(func() { close(inner.release) })
		<-added
		if _, ok := p.clients["server"]; !ok {
			t.Fatal("server not re-added after transport close")
		}
	})
}

func TestStopAllWaitsForRemovedOrDisabledTransport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		servers map[string]config.MCPServerConfig
	}{
		{name: "remove"},
		{name: "disable", servers: map[string]config.MCPServerConfig{"server": {Disabled: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				inner := &gatedConfigClose{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
				var release sync.Once
				defer release.Do(func() { close(inner.release) })
				p := NewPool("", false, false)
				cfg := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid/server"}
				p.clients["server"] = &managedClient{name: "server", cfg: cfg, client: &Client{inner: inner, ready: true}}
				p.lifecycle("server")
				p.UpdateFromConfig(tc.servers, nil)
				<-inner.entered
				stopped := make(chan struct{})
				go func() { p.StopAll(); close(stopped) }()
				synctest.Wait()
				select {
				case <-stopped:
					t.Fatal("StopAll returned before retired transport closed")
				default:
				}
				p.HasTool("server")
				p.ListTools()
				p.ServerStates()
				release.Do(func() { close(inner.release) })
				<-stopped
			})
		})
	}
}

type gatedFailedCall struct {
	*gatedConfigClose
	callEntered chan struct{}
	releaseCall chan struct{}
	cause       error
	calls       atomic.Int32
}

func (c *gatedFailedCall) CallTool(_ context.Context, _ protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	c.calls.Add(1)
	close(c.callEntered)
	<-c.releaseCall
	return nil, c.cause
}

func TestFailedCallCloseDoesNotBlockConfigOrReaders(t *testing.T) {
	cause := errors.New("synthetic transport failure")
	inner := &gatedFailedCall{gatedConfigClose: &gatedConfigClose{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), cause: cause}
	var callRelease, closeRelease sync.Once
	t.Cleanup(func() {
		callRelease.Do(func() { close(inner.releaseCall) })
		closeRelease.Do(func() { close(inner.release) })
	})
	p := NewPool("", false, false)
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://example.invalid/old"}
	p.clients["server"] = &managedClient{name: "server", cfg: cfg, client: &Client{inner: inner, ready: true}}
	life := p.lifecycle("server")
	called := make(chan error, 1)
	go func() { _, err := p.CallTool(context.Background(), "server", nil); called <- err }()
	select {
	case <-inner.callEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not start")
	}
	updated := make(chan struct{})
	go func() {
		p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: "http://example.invalid/new"}}, nil)
		close(updated)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		life.mu.Lock()
		reserved := life.closing != nil
		life.mu.Unlock()
		if reserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("config did not reserve lifecycle before call completed")
		}
		runtime.Gosched()
	}
	callRelease.Do(func() { close(inner.releaseCall) })
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	select {
	case err := <-called:
		if !errors.Is(err, cause) {
			t.Fatalf("call error = %v, want original cause", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed call blocked on transport Close")
	}
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("config update blocked on failed-call Close")
	}
	readers := make(chan struct{})
	go func() { p.HasTool("server"); p.ListTools(); p.ServerStates(); close(readers) }()
	select {
	case <-readers:
	case <-time.After(time.Second):
		t.Fatal("pool readers blocked on failed-call Close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := life.begin(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new start bypassed failed-call close fence: %v", err)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("tool invoked %d times, want one", got)
	}
	closeRelease.Do(func() { close(inner.release) })
	restartCtx, restartCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer restartCancel()
	if err := life.begin(restartCtx); err != nil {
		t.Fatal(err)
	}
	life.finishStart()
}

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
