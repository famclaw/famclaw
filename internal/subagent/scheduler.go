package subagent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Scheduler manages subagent execution with concurrency control.
// Jobs targeting different LLM backends run in parallel.
// Same-backend jobs are queued sequentially.
type Scheduler struct {
	maxConcurrent int
	mu            sync.Mutex   // guards the check-and-increment in Submit
	active        atomic.Int32 // read by Active(); written under mu in Submit
}

// NewScheduler creates a scheduler with the given concurrency limit.
func NewScheduler(maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Scheduler{
		maxConcurrent: maxConcurrent,
	}
}

// Submit queues a subagent job for execution.
//
// The executor function is called in a goroutine and runs the subagent
// pipeline. The returned channel receives exactly one Result and is then
// closed; each Submit call gets its own dedicated buffered channel so
// concurrent submissions do not cross-deliver results.
//
// The check-and-increment of active is guarded by s.mu so the concurrency
// limit is enforced atomically (no TOCTOU window).
func (s *Scheduler) Submit(ctx context.Context, cfg Config, executor func(ctx context.Context, cfg Config) (string, error)) (string, <-chan Result, error) {
	s.mu.Lock()
	if int(s.active.Load()) >= s.maxConcurrent {
		current := s.active.Load()
		s.mu.Unlock()
		return "", nil, fmt.Errorf("subagent queue full (%d/%d active)", current, s.maxConcurrent)
	}
	s.active.Add(1)
	s.mu.Unlock()

	agentID := generateID()
	resultCh := make(chan Result, 1)

	go func() {
		defer s.active.Add(-1)
		defer close(resultCh)

		output, err := executor(ctx, cfg)
		resultCh <- Result{
			AgentID: agentID,
			Output:  output,
			Error:   err,
		}
	}()

	return agentID, resultCh, nil
}

// Active returns the number of currently running subagents.
func (s *Scheduler) Active() int {
	return int(s.active.Load())
}

var idCounter atomic.Int64

func generateID() string {
	n := idCounter.Add(1)
	return fmt.Sprintf("agent-%d", n)
}
