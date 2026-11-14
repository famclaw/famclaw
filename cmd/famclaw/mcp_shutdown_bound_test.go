package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownMCPHonorsDeadlineWhileStopContinues(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("stop did not finish after release")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- shutdownMCP(ctx, func() {
			close(entered)
			<-release
			close(finished)
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stop did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after cancellation")
	}
	select {
	case <-finished:
		t.Fatal("blocked stop unexpectedly finished")
	default:
	}
}

func TestShutdownMCPWaitsForCompletedStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdownMCP(ctx, func() {}); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
}
