package mcpserver

import (
	"fmt"
	"sync"
	"time"
)

// rateLimits holds the per-tool per-second caps from the task spec
// (design §5.4 gives turn-level caps; the card's per-tool caps are the
// v1 enforcement): tap 5/s, type 30/s, screencap 2/s.
var rateLimits = map[string]int{
	"tap":        5,
	"type":       30,
	"screenshot": 2,
}

// rateLimiter enforces the per-tool per-second caps with a sliding 1-second
// window per tool. It is safe for concurrent use (the MCP server is
// single-client, but handlers may run on separate goroutines).
type rateLimiter struct {
	mu    sync.Mutex
	calls map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{calls: make(map[string][]time.Time)}
}

// allow records the call if it is within the cap for the tool and returns
// nil, or a rate-limit error otherwise. Tools without a configured cap are
// always allowed.
func (rl *rateLimiter) allow(tool string) error {
	limit, ok := rateLimits[tool]
	if !ok {
		return nil
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-time.Second)

	recent := rl.calls[tool]
	// Compact: drop entries older than the window. Entries are in
	// chronological order (append-only), so stale entries form a prefix.
	keep := 0
	for keep < len(recent) && !recent[keep].After(cutoff) {
		keep++
	}
	recent = recent[keep:]

	if len(recent) >= limit {
		return fmt.Errorf("rate limit: at most %d %s calls per second", limit, tool)
	}

	rl.calls[tool] = append(recent, now)
	return nil
}

// reset clears all recorded call times (used by tests).
func (rl *rateLimiter) reset() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.calls = make(map[string][]time.Time)
}
