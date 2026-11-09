package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/famclaw/famclaw/internal/config"
)

type blockedCloseClient struct {
	mcpclient.MCPClient
	entered chan struct{}
	release chan struct{}
}

func (c *blockedCloseClient) Close() error {
	close(c.entered)
	<-c.release
	return nil
}

func TestStopAllAllowsCachedLookupDuringTransportClose(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	t.Cleanup(func() { close(inner.release) })
	done := make(chan struct{})
	go func() { p.StopAll(); close(done) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	lookup := make(chan bool, 1)
	go func() { lookup <- p.HasTool("server") }()
	select {
	case ok := <-lookup:
		if !ok {
			t.Fatal("cached route lost")
		}
	case <-time.After(time.Second):
		t.Fatal("cached lookup blocked during transport Close")
	}
}

func TestStopAllFencesSameServerAndLeavesReadersAvailable(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	t.Cleanup(func() { close(inner.release) })
	first, second := make(chan struct{}), make(chan struct{})
	go func() { p.StopAll(); close(first) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	go func() { p.StopAll(); close(second) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.CallTool(ctx, "server", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-server call should wait and honor cancellation: %v", err)
	}
	available := make(chan struct{})
	go func() { p.ListTools(); p.ListToolInfos(); close(available) }()
	select {
	case <-available:
	case <-time.After(time.Second):
		t.Fatal("cached discovery blocked")
	}
	select {
	case <-first:
		t.Fatal("first stop returned while close blocked")
	default:
	}
	select {
	case <-second:
		t.Fatal("second stop lost outstanding close")
	default:
	}
}

func TestConfigRemovalWaitsOutsidePoolLockForClose(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	t.Cleanup(func() { close(inner.release) })
	go p.StopAll()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	updated := make(chan struct{})
	go func() { p.UpdateFromConfig(nil, nil); close(updated) }()
	select {
	case <-updated:
		t.Fatal("config removed server before close returned")
	case <-time.After(100 * time.Millisecond):
	}
	lookup := make(chan bool, 1)
	go func() { lookup <- p.HasTool("server") }()
	select {
	case <-lookup:
	case <-time.After(time.Second):
		t.Fatal("config wait held pool lock")
	}
}

type failingCallAndClose struct {
	mcpclient.MCPClient
	calls int
}

var errOriginalCall = errors.New("original call failure")

func (c *failingCallAndClose) CallTool(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	c.calls++
	return nil, errOriginalCall
}
func (c *failingCallAndClose) Close() error { return errors.New("secondary close failure") }

func TestFailedCallCloseErrorReleasesFenceWithoutReplay(t *testing.T) {
	c := newTestClient(t)
	inner := &failingCallAndClose{MCPClient: c.inner}
	c.inner = inner
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	_, err := p.CallTool(context.Background(), "server", nil)
	if !errors.Is(err, errOriginalCall) {
		t.Fatalf("original cause lost: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = p.CallTool(ctx, "server", nil)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("failed Close left the fence closed")
	}
	if inner.calls != 1 {
		t.Fatalf("failed call replayed: %d", inner.calls)
	}
}

func TestConfigReplacementWaitsForInFlightStartupOutsidePoolLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		http.Error(w, "offline test", http.StatusInternalServerError)
	}))
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); srv.Close() })
	p := NewPool("", false, false)
	old := config.MCPServerConfig{Transport: "http", URL: srv.URL}
	p.RegisterFromConfig(map[string]config.MCPServerConfig{"server": old}, nil)
	started := make(chan struct{})
	go func() { _ = p.StartAll(context.Background()); close(started) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not reach server")
	}
	changed := make(chan struct{})
	go func() {
		p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: srv.URL + "/replacement"}}, nil)
		close(changed)
	}()
	select {
	case <-changed:
		t.Fatal("config replaced server during startup")
	case <-time.After(100 * time.Millisecond):
	}
	lookup := make(chan bool, 1)
	go func() { lookup <- p.HasTool("server") }()
	select {
	case <-lookup:
	case <-time.After(time.Second):
		t.Fatal("configuration wait blocked cached lookup")
	}
	once.Do(func() { close(release) })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("startup did not finish")
	}
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("configuration did not finish")
	}
}

func TestStartAllStartsUnrelatedServerWhileAnotherCloses(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(inner.release) }) })
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		http.Error(w, "offline test", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := NewPool("", false, false)
	p.clients["blocked"] = &managedClient{name: "blocked", client: c}
	cfg := config.MCPServerConfig{Transport: "http", URL: srv.URL}
	p.clients["other"] = &managedClient{name: "other", cfg: cfg, client: NewTransportClient("other", cfg, "", false, false)}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	go func() { _ = p.StartAll(ctx); close(started) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated startup was blocked by Close")
	}

	once.Do(func() { close(inner.release) })
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("StartAll did not finish")
	}
}

func TestRemovedServerCanBeReaddedOnlyAfterPriorClose(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(inner.release) }) })
	p := NewPool("", false, false)
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://127.0.0.1:1"}
	p.clients["server"] = &managedClient{name: "server", cfg: cfg, client: c}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	done := make(chan struct{})
	go func() {
		p.UpdateFromConfig(nil, nil)
		p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": cfg}, nil)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("removed and re-added before close finished")
	case <-time.After(100 * time.Millisecond):
	}
	once.Do(func() { close(inner.release) })
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-add did not finish")
	}
	if !p.HasTool("server") {
		t.Fatal("re-added server missing")
	}
}

func TestRegisteredUnrelatedToolCallProgressesDuringClose(t *testing.T) {
	c := newTestClient(t)
	inner := &blockedCloseClient{MCPClient: c.inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.inner = inner
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(inner.release) }) })
	calls := make(chan struct{}, 1)
	backend := server.NewMCPServer("secondary", "1", server.WithToolCapabilities(true))
	backend.AddTool(protocol.NewTool("independent"), func(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		calls <- struct{}{}
		return protocol.NewToolResultText("ok"), nil
	})
	srv := httptest.NewServer(server.NewStreamableHTTPServer(backend))
	t.Cleanup(srv.Close)
	cfg := config.MCPServerConfig{Transport: "http", URL: srv.URL}
	p := NewPool("", false, false)
	p.clients["blocked"] = &managedClient{name: "blocked", client: c}
	p.clients["other"] = &managedClient{name: "other", cfg: cfg, client: NewTransportClient("other", cfg, "", false, false)}
	if err := p.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.HasTool("independent") {
		t.Fatal("unrelated tool not registered")
	}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-inner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.CallTool(ctx, "independent", nil); err != nil {
		t.Fatalf("unrelated call: %v", err)
	}
	select {
	case <-calls:
	default:
		t.Fatal("unrelated call not delivered")
	}
	once.Do(func() { close(inner.release) })
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
}

type closeFailure struct{ mcpclient.MCPClient }

func (c *closeFailure) Close() error { return errors.New("secondary close failure") }
func TestStopAllCloseErrorReleasesFence(t *testing.T) {
	c := newTestClient(t)
	c.inner = &closeFailure{MCPClient: c.inner}
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	p.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := p.CallTool(ctx, "server", nil)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Close error left the server fenced")
	}
}

func TestCachedListDoesNotPreventStopOfActiveCall(t *testing.T) {
	c := newTestClient(t)
	f := &shutdownRaceInner{MCPClient: c.inner, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), closeEntered: make(chan struct{}, 1), releaseClose: make(chan struct{})}
	c.inner = f
	var callOnce, closeOnce sync.Once
	t.Cleanup(func() { callOnce.Do(func() { close(f.releaseCall) }); closeOnce.Do(func() { close(f.releaseClose) }) })
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	go func() { _, _ = p.CallTool(context.Background(), "server", nil) }()
	select {
	case <-f.callEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not start")
	}
	listed := make(chan struct{})
	go func() { p.ListTools(); close(listed) }()
	deadline := time.Now().Add(2 * time.Second)
waitForList:
	for {
		select {
		case <-listed:
			break waitForList
		default:
		}
		if !p.mu.TryLock() {
			break
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("listing did not begin")
		}
		runtime.Gosched()
	}
	go p.StopAll()
	select {
	case <-f.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("cached listing prevented transport Close")
	}
	callOnce.Do(func() { close(f.releaseCall) })
	closeOnce.Do(func() { close(f.releaseClose) })
}

func TestServerStatesDoesNotPreventStopOfActiveCall(t *testing.T) {
	c := newTestClient(t)
	f := &shutdownRaceInner{MCPClient: c.inner, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), closeEntered: make(chan struct{}, 1), releaseClose: make(chan struct{})}
	c.inner = f
	var callOnce, closeOnce sync.Once
	t.Cleanup(func() { callOnce.Do(func() { close(f.releaseCall) }); closeOnce.Do(func() { close(f.releaseClose) }) })
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	go func() { _, _ = p.CallTool(context.Background(), "server", nil) }()
	select {
	case <-f.callEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not start")
	}
	listed := make(chan struct{})
	go func() { p.ServerStates(); close(listed) }()
	deadline := time.Now().Add(2 * time.Second)
waitForStates:
	for {
		select {
		case <-listed:
			break waitForStates
		default:
		}
		if !p.mu.TryLock() {
			break
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("states did not begin")
		}
		runtime.Gosched()
	}
	go p.StopAll()
	select {
	case <-f.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("server states prevented transport Close")
	}
	callOnce.Do(func() { close(f.releaseCall) })
	closeOnce.Do(func() { close(f.releaseClose) })
}

func TestConfigRetirementReservesStartFenceBeforeWaitingForCall(t *testing.T) {
	c := newTestClient(t)
	p := NewPool("", false, false)
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://127.0.0.1:1"}
	mc := &managedClient{name: "server", cfg: cfg, client: c}
	p.clients["server"] = mc
	p.mu.Lock()
	life := p.lifecycle("server")
	p.mu.Unlock()
	mc.mu.Lock()
	var once sync.Once
	t.Cleanup(func() { once.Do(mc.mu.Unlock) })
	done := make(chan struct{})
	go func() {
		p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: "http://127.0.0.1:2"}}, nil)
		close(done)
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
			t.Fatal("config did not reserve start fence")
		}
		runtime.Gosched()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := life.begin(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			life.finishStart()
		}
		t.Fatalf("captured start bypassed config reservation: %v", err)
	}
	once.Do(mc.mu.Unlock)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("configuration did not finish")
	}
}

func TestLazyCallAndConfigReservationDoNotDeadlock(t *testing.T) {
	c := newTestClient(t)
	f := &shutdownRaceInner{MCPClient: c.inner, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), closeEntered: make(chan struct{}, 1), releaseClose: make(chan struct{})}
	c.inner = f
	c.ready = false
	p := NewPool("", false, false)
	cfg := config.MCPServerConfig{Transport: "http", URL: "http://127.0.0.1:1"}
	mc := &managedClient{name: "server", cfg: cfg, client: c}
	p.clients["server"] = mc
	p.mu.Lock()
	life := p.lifecycle("server")
	p.mu.Unlock()
	c.mu.Lock()
	var clientOnce, callOnce, closeOnce sync.Once
	t.Cleanup(func() {
		clientOnce.Do(c.mu.Unlock)
		callOnce.Do(func() { close(f.releaseCall) })
		closeOnce.Do(func() { close(f.releaseClose) })
	})
	called := make(chan struct{})
	go func() { _, _ = p.CallTool(context.Background(), "server", nil); close(called) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !mc.mu.TryLock() {
			break
		}
		mc.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("call did not acquire client lock")
		}
		runtime.Gosched()
	}
	changed := make(chan struct{})
	go func() {
		p.UpdateFromConfig(map[string]config.MCPServerConfig{"server": {Transport: "http", URL: "http://127.0.0.1:2"}}, nil)
		close(changed)
	}()
	deadline = time.Now().Add(2 * time.Second)
	for {
		life.mu.Lock()
		reserved := life.closing != nil
		life.mu.Unlock()
		if reserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("config did not reserve fence")
		}
		runtime.Gosched()
	}
	clientOnce.Do(c.mu.Unlock)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("call and config reservation deadlocked")
	}
	callOnce.Do(func() { close(f.releaseCall) })
	closeOnce.Do(func() { close(f.releaseClose) })
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("config did not finish")
	}
}

func TestQueuedCallBeforeStopCannotRestartOldGeneration(t *testing.T) {
	c := newTestClient(t)
	f := &shutdownRaceInner{MCPClient: c.inner, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), closeEntered: make(chan struct{}, 1), releaseClose: make(chan struct{})}
	c.inner = f
	p := NewPool("", false, false)
	p.clients["server"] = &managedClient{name: "server", client: c}
	var callOnce, closeOnce sync.Once
	t.Cleanup(func() { callOnce.Do(func() { close(f.releaseCall) }); closeOnce.Do(func() { close(f.releaseClose) }) })
	first := make(chan struct{})
	go func() { _, _ = p.CallTool(context.Background(), "server", nil); close(first) }()
	select {
	case <-f.callEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first call did not start")
	}
	second := make(chan error, 1)
	go func() { _, err := p.CallTool(context.Background(), "server", nil); second <- err }()
	p.mu.Lock()
	life := p.lifecycle("server")
	p.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		life.mu.Lock()
		pending := life.starts
		life.mu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second call was not admitted")
		}
		runtime.Gosched()
	}
	stopped := make(chan struct{})
	go func() { p.StopAll(); close(stopped) }()
	select {
	case <-f.closeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close was not initiated")
	}
	closeOnce.Do(func() { close(f.releaseClose) })
	callOnce.Do(func() { close(f.releaseCall) })
	select {
	case err := <-second:
		if !errors.Is(err, errClientStopped) {
			t.Fatalf("stale queued call: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued call did not finish")
	}
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("first call did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
}
