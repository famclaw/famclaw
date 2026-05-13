package toolcache

import (
	"context"
	"log"
	"time"
)

// StartSweeper launches the background sweeper goroutine. Idempotent —
// calling twice does nothing the second time. Call StopSweeper to halt.
func (c *Cache) StartSweeper(interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	c.sweepMu.Lock()
	if c.sweepStop != nil {
		c.sweepMu.Unlock()
		return
	}
	stop := make(chan struct{})
	c.sweepStop = stop
	c.sweepMu.Unlock()

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if _, err := c.Sweep(context.Background()); err != nil {
					log.Printf("[toolcache] sweep error: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()
}

// StopSweeper signals the sweeper goroutine to exit. Idempotent.
func (c *Cache) StopSweeper() {
	c.sweepMu.Lock()
	defer c.sweepMu.Unlock()
	if c.sweepStop == nil {
		return
	}
	close(c.sweepStop)
	c.sweepStop = nil
}
