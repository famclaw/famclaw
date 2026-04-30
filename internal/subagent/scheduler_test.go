package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerSubmitAndResult(t *testing.T) {
	s := NewScheduler(5)

	id, ch, err := s.Submit(context.Background(), Config{Prompt: "test"}, func(ctx context.Context, cfg Config) (string, error) {
		return "hello from subagent", nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty agent ID")
	}

	select {
	case result := <-ch:
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

	_, ch, err := s.Submit(context.Background(), Config{Prompt: "fail"}, func(ctx context.Context, cfg Config) (string, error) {
		return "", wantErr
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case result := <-ch:
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
	chans := make([]<-chan Result, 0, 2)
	for i := 0; i < 2; i++ {
		_, ch, err := s.Submit(context.Background(), Config{}, func(ctx context.Context, cfg Config) (string, error) {
			<-blocker
			return "done", nil
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		chans = append(chans, ch)
	}

	// Third should fail
	_, _, err := s.Submit(context.Background(), Config{}, func(ctx context.Context, cfg Config) (string, error) {
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

	// Wait for completion on each per-call channel
	for _, ch := range chans {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for blocked agents")
		}
	}
}

func TestSchedulerParallelExecution(t *testing.T) {
	s := NewScheduler(10)
	var mu sync.Mutex
	var completionOrder []string

	chans := make([]<-chan Result, 0, 5)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("agent-%d", i)
		_, ch, err := s.Submit(context.Background(), Config{Prompt: name}, func(ctx context.Context, cfg Config) (string, error) {
			// Simulate varying work
			time.Sleep(time.Duration(5-len(cfg.Prompt)%3) * time.Millisecond)
			mu.Lock()
			completionOrder = append(completionOrder, cfg.Prompt)
			mu.Unlock()
			return cfg.Prompt, nil
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		chans = append(chans, ch)
	}

	for _, ch := range chans {
		select {
		case <-ch:
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

// TestSchedulerConcurrencyRace stresses the check-and-increment under contention.
// With 50 concurrent Submit attempts and a max of 2, the observed in-flight
// counter must never exceed 2. Without the mutex around the check+increment,
// this test reliably races past the limit.
func TestSchedulerConcurrencyRace(t *testing.T) {
	const (
		max     = 2
		callers = 50
	)
	s := NewScheduler(max)

	var inFlight atomic.Int32
	var maxObserved atomic.Int32
	release := make(chan struct{})

	executor := func(ctx context.Context, cfg Config) (string, error) {
		cur := inFlight.Add(1)
		// Track max observed concurrency.
		for {
			prev := maxObserved.Load()
			if cur <= prev || maxObserved.CompareAndSwap(prev, cur) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return "ok", nil
	}

	var wg sync.WaitGroup
	var accepted atomic.Int32
	var chans sync.Map // agentID -> <-chan Result

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ch, err := s.Submit(context.Background(), Config{}, executor)
			if err == nil {
				accepted.Add(1)
				chans.Store(id, ch)
			}
		}()
	}
	wg.Wait()

	if got := accepted.Load(); got > max {
		t.Fatalf("accepted %d submissions, want <= %d", got, max)
	}

	close(release)

	// Drain the result channels we accepted.
	chans.Range(func(_, v any) bool {
		ch := v.(<-chan Result)
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout draining accepted channel")
		}
		return true
	})

	if got := maxObserved.Load(); got > max {
		t.Errorf("max observed concurrency = %d, want <= %d", got, max)
	}
}

// TestSchedulerNoCrossDelivery proves each Submit caller receives only its own
// result. Submitting two jobs A (slow) and B (fast) at the same time, the
// slow caller's channel must yield the slow agent's ID — not the fast one's.
func TestSchedulerNoCrossDelivery(t *testing.T) {
	s := NewScheduler(5)

	idA, chA, err := s.Submit(context.Background(), Config{Prompt: "A"}, func(ctx context.Context, cfg Config) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "A-output", nil
	})
	if err != nil {
		t.Fatalf("Submit A: %v", err)
	}
	idB, chB, err := s.Submit(context.Background(), Config{Prompt: "B"}, func(ctx context.Context, cfg Config) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "B-output", nil
	})
	if err != nil {
		t.Fatalf("Submit B: %v", err)
	}

	if idA == idB {
		t.Fatalf("agent IDs should differ: A=%s B=%s", idA, idB)
	}

	select {
	case rB := <-chB:
		if rB.AgentID != idB {
			t.Errorf("chB delivered wrong agent: got %s, want %s", rB.AgentID, idB)
		}
		if rB.Output != "B-output" {
			t.Errorf("chB output = %q, want B-output", rB.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting on chB")
	}

	select {
	case rA := <-chA:
		if rA.AgentID != idA {
			t.Errorf("chA delivered wrong agent: got %s, want %s", rA.AgentID, idA)
		}
		if rA.Output != "A-output" {
			t.Errorf("chA output = %q, want A-output", rA.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting on chA")
	}
}

// TestSchedulerSubmitContextDeadline ensures a context with a tight deadline
// surfaces context.DeadlineExceeded to the caller via the per-call channel.
// (This is the executor's responsibility to honor the context — the scheduler
// just plumbs it through.)
func TestSchedulerSubmitContextDeadline(t *testing.T) {
	s := NewScheduler(5)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, ch, err := s.Submit(ctx, Config{}, func(ctx context.Context, cfg Config) (string, error) {
		select {
		case <-time.After(2 * time.Second):
			return "should not happen", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case r := <-ch:
		if !errors.Is(r.Error, context.DeadlineExceeded) {
			t.Errorf("expected DeadlineExceeded, got %v", r.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for deadline result")
	}
}
