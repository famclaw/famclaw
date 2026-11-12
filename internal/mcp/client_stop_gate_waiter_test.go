package mcp

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestClientStopWhileStartWaitsForGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Hold the token as an in-progress handshake would, without transport I/O.
		gate := make(chan struct{}, 1)
		c := &Client{startGate: gate}
		startResult := make(chan error, 1)
		go func() { startResult <- c.Start(context.Background()) }()
		synctest.Wait() // Start has captured the generation and is waiting for the gate.

		stopped := make(chan struct{})
		go func() {
			c.Stop()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Error("Stop did not complete while Start waited for the gate")
		}

		gate <- struct{}{}
		if err := <-startResult; !errors.Is(err, errClientStopped) {
			t.Errorf("Start after Stop returned %v, want errClientStopped", err)
		}
		<-stopped
	})
}
