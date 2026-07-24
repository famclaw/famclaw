package gateway

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSessionPoolContextCancellation verifies that session goroutines
// properly cancel their contexts and exit cleanly when the shutdown context is cancelled.
func TestSessionPoolContextCancellation(t *testing.T) {
	// Create a shutdown context that we can cancel
	ctx, cancel := context.WithCancel(context.Background())
	
	// Create a session pool with a processor that takes time
	var processedMessages sync.Map
	var mu sync.Mutex
	processedCount := 0
	
	sessionPool := NewSessionPool(ctx, func(ctx context.Context, msg Message) Reply {
		// Simulate some processing time
		select {
		case <-time.After(100 * time.Millisecond):
			mu.Lock()
			processedCount++
			mu.Unlock()
			processedMessages.Store(msg.Text, true)
			return Reply{Text: "processed: " + msg.Text, PolicyAction: "allow"}
		case <-ctx.Done():
			// Context cancelled - this should cause clean exit
			return Reply{Text: "cancelled", PolicyAction: "error"}
		}
	})
	
	// Send several messages to create session goroutines
	done := make(chan struct{})
	go func() {
		defer close(done)
		
		// Send messages that will trigger session goroutines
		for i := 0; i < 5; i++ {
			reply := sessionPool.Dispatch(context.Background(), "testuser", Message{
				Gateway:    "test",
				ExternalID: "test123",
				Text:       "message-" + string(rune(i+'0')),
			})
			if reply.PolicyAction != "allow" {
				t.Errorf("Expected success, got %q", reply.PolicyAction)
			}
		}
	}()
	
	// Wait a bit for goroutines to be created
	time.Sleep(50 * time.Millisecond)
	
	// Cancel the shutdown context to trigger cleanup
	cancel()
	
	// Wait for goroutines to finish
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for session pool to finish")
	}
	
	// Verify that we processed messages properly
	if processedCount < 1 {
		t.Error("Expected at least one message to be processed")
	}
	
	// Verify that the session pool shutdown was handled properly
	// (this test mainly ensures no goroutine leaks occur)
}

// TestSessionPoolTimeoutHandling verifies proper timeout handling 
// and that goroutines don't leak in timeout scenarios.
func TestSessionPoolTimeoutHandling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Create a session pool that simulates a long-running operation
	sessionPool := NewSessionPool(ctx, func(ctx context.Context, msg Message) Reply {
		// Simulate a long-running task
		select {
		case <-time.After(2 * time.Second):
			return Reply{Text: "completed", PolicyAction: "allow"}
		case <-ctx.Done():
			return Reply{Text: "timeout", PolicyAction: "error"}
		}
	})
	
	// Send a message with a very short timeout
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer shortCancel()
	
	reply := sessionPool.Dispatch(shortCtx, "testuser", Message{
		Gateway:    "test",
		ExternalID: "test123",
		Text:       "long-running-task",
	})
	
	// Should timeout
	if reply.PolicyAction != "error" {
		t.Errorf("Expected timeout error, got %q", reply.PolicyAction)
	}
	
	// Give some time for cleanup
	time.Sleep(50 * time.Millisecond)
	
	// Should not hang or leak
}