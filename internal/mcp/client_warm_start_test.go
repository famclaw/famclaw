package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
)

type countingWarmClient struct {
	mcpclient.MCPClient
	calls atomic.Int32
}

func (c *countingWarmClient) CallTool(ctx context.Context, request protocol.CallToolRequest) (*protocol.CallToolResult, error) {
	c.calls.Add(1)
	return c.MCPClient.CallTool(ctx, request)
}

func TestWarmCallBypassesHeldStartupGateAndHonorsCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		wantCalls int32
	}{
		{name: "warm call", wantCalls: 1},
		{name: "cancelled warm call", cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t)
			inner := &countingWarmClient{MCPClient: c.inner}
			c.inner = inner
			// A completed handshake can have marked the client ready before its
			// deferred gate release. Hold the token to reproduce that window.
			c.startGate = make(chan struct{}, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			result, err := c.CallTool(ctx, "echo", map[string]any{"text": "hello"})
			if tc.cancelled {
				if !errors.Is(err, context.Canceled) || result != nil {
					t.Fatalf("cancelled call result=%v err=%v", result, err)
				}
			} else if err != nil || result == nil {
				t.Fatalf("ready call should not wait for startup gate: result=%v err=%v", result, err)
			}
			if got := inner.calls.Load(); got != tc.wantCalls {
				t.Errorf("transport calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func BenchmarkClientWarmStart(b *testing.B) {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	c := &Client{inner: &countingWarmClient{}, ready: true, startGate: gate}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.start(ctx, true); err != nil {
			b.Fatal(err)
		}
	}
}
