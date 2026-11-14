package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
)

type shutdownRaceInner struct {
	mcpclient.MCPClient
	callEntered  chan struct{}
	releaseCall  chan struct{}
	closeEntered chan struct{}
	releaseClose chan struct{}
}

func (f *shutdownRaceInner) CallTool(context.Context, protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	close(f.callEntered)
	<-f.releaseCall
	return nil, errors.New("fake transport failure")
}

func (f *shutdownRaceInner) Close() error {
	f.closeEntered <- struct{}{}
	<-f.releaseClose
	return nil
}

func TestPoolStopAllConcurrentFailedCall(t *testing.T) {
	c := newTestClient(t)
	f := &shutdownRaceInner{MCPClient: c.inner, callEntered: make(chan struct{}), releaseCall: make(chan struct{}), closeEntered: make(chan struct{}, 2), releaseClose: make(chan struct{})}
	c.inner = f
	p := NewPool("", false, false)
	p.clients["echo"] = &managedClient{name: "mock", client: c}
	var onceCall, onceClose sync.Once
	t.Cleanup(func() { onceCall.Do(func() { close(f.releaseCall) }); onceClose.Do(func() { close(f.releaseClose) }) })
	doneCall, doneStop := make(chan struct{}), make(chan struct{})
	go func() { defer close(doneCall); _, _ = p.CallTool(context.Background(), "echo", nil) }()
	select {
	case <-f.callEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not enter")
	}
	go func() { defer close(doneStop); p.StopAll() }()
	select {
	case <-f.closeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not enter close")
	}
	onceCall.Do(func() { close(f.releaseCall) })
	select {
	case <-doneCall:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not finish while transport close was in progress")
	}
	select {
	case <-f.closeEntered:
		t.Fatal("transport closed twice")
	default:
	}
	onceClose.Do(func() { close(f.releaseClose) })
	select {
	case <-doneStop:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish")
	}
}
