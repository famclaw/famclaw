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
	active        atomic.Int32
	mu            sync.Mutex
	results       chan Result
}

// NewScheduler creates a scheduler with the given concurrency limit.
func NewScheduler(maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Scheduler{
		maxConcurrent: maxConcurrent,
		results:       make(chan Result, 100),
	}
}

// Submit queues a subagent job for execution.
// The executor function is called in a goroutine and should run the subagent pipeline.
// Returns the agent ID for tracking.
func (s *Scheduler) Submit(ctx context.Context, cfg Config, executor func(ctx context.Context, cfg Config) (string, error)) (string, error) {
	if int(s.active.Load()) >= s.maxConcurrent {
		return "", fmt.Errorf("subagent queue full (%d/%d active)", s.active.Load(), s.maxConcurrent)
	}

	agentID := generateID()
	s.active.Add(1)

	go func() {
		defer s.active.Add(-1)

		output, err := executor(ctx, cfg)
		s.results <- Result{
			AgentID: agentID,
			Output:  output,
			Error:   err,
		}
	}()

	return agentID, nil
}

// Results returns the channel where completed subagent results are sent.
func (s *Scheduler) Results() <-chan Result {
	return s.results
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
