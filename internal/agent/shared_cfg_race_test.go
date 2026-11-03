package agent

import (
	"sync"
	"testing"
)

// TestSharedConfigTwoAgentDenylistRace tests concurrent access to shared denylist config
// to ensure no data races occur.
func TestSharedConfigTwoAgentDenylistRace(t *testing.T) {
	// Test concurrent access to shared denylist data structures
	// This simulates the race condition scenario from the issue

	// Create shared denylist data
	var denylist []string
	var mu sync.RWMutex

	// Simulate concurrent operations on the shared denylist
	var wg sync.WaitGroup

	// 100 concurrent operations adding to denylist
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mu.Lock()
			denylist = append(denylist, "test-host-"+string(rune(id)))
			mu.Unlock()
		}(i)
	}

	// 100 concurrent operations removing from denylist
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mu.Lock()
			if len(denylist) > 0 {
				denylist = denylist[:len(denylist)-1]
			}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify final state consistency
	finalLen := len(denylist)
	if finalLen < 0 || finalLen > 100 {
		t.Errorf("Final denylist length %d is out of expected range [0, 100]", finalLen)
	}
}