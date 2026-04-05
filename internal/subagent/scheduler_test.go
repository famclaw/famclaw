package subagent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSchedulerSubmitAndResult(t *testing.T) {
	s := NewScheduler(5)

	id, err := s.Submit(context.Background(), Config{Prompt: "test"}, func(ctx context.Context, cfg Config) (string, error) {
		return "hello from subagent", nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty agent ID")
	}

	select {
	case result := <-s.Results():
		if result.AgentID != id {
			t.Errorf("result.AgentID = %q, want %q", result.AgentID, id)
		}
		if result.Output != "hello from subagent" {
			t.Errorf("result.Output = %q", result.Output)
		}
		if result.Error != nil {
			t.Errorf("unexpected error: %v", result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestSchedulerSubmitError(t *testing.T) {
	s := NewScheduler(5)
	wantErr := fmt.Errorf("something went wrong")

	s.Submit(context.Background(), Config{Prompt: "fail"}, func(ctx context.Context, cfg Config) (string, error) {
		return "", wantErr
	})

	select {
	case result := <-s.Results():
		if result.Error == nil || result.Error.Error() != wantErr.Error() {
			t.Errorf("expected error %q, got %v", wantErr, result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSchedulerConcurrencyLimit(t *testing.T) {
	s := NewScheduler(2)

	// Fill both slots
	blocker := make(chan struct{})
	for i := 0; i < 2; i++ {
		_, err := s.Submit(context.Background(), Config{}, func(ctx context.Context, cfg Config) (string, error) {
			<-blocker
			return "done", nil
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}

	// Third should fail
	_, err := s.Submit(context.Background(), Config{}, func(ctx context.Context, cfg Config) (string, error) {
		return "should not run", nil
	})
	if err == nil {
		t.Error("expected queue full error")
	}

	if s.Active() != 2 {
		t.Errorf("Active() = %d, want 2", s.Active())
	}

	// Unblock
	close(blocker)

	// Wait for completion
	for i := 0; i < 2; i++ {
		select {
		case <-s.Results():
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for blocked agents")
		}
	}
}

func TestSchedulerParallelExecution(t *testing.T) {
	s := NewScheduler(10)
	var mu sync.Mutex
	var completionOrder []string

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("agent-%d", i)
		s.Submit(context.Background(), Config{Prompt: name}, func(ctx context.Context, cfg Config) (string, error) {
			// Simulate varying work
			time.Sleep(time.Duration(5-len(cfg.Prompt)%3) * time.Millisecond)
			mu.Lock()
			completionOrder = append(completionOrder, cfg.Prompt)
			mu.Unlock()
			return cfg.Prompt, nil
		})
	}

	for i := 0; i < 5; i++ {
		select {
		case <-s.Results():
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
	}

	if len(completionOrder) != 5 {
		t.Errorf("expected 5 completions, got %d", len(completionOrder))
	}
}

func TestSchedulerDefaultConcurrency(t *testing.T) {
	s := NewScheduler(0) // should default to 1
	if s.maxConcurrent != 1 {
		t.Errorf("maxConcurrent = %d, want 1", s.maxConcurrent)
	}
}
