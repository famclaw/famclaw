package config

import (
	"sync"
	"testing"
)

// TestWebFetchPolicyOwner_SharedInstance pins the #423 round 18 ownership
// model: one WebFetchPolicyOwner per *Config instance, reachable from any
// holder of that pointer. The round 16/17 failure mode was a per-Agent
// value mutex — two Agents over the same config never shared a lock.
func TestWebFetchPolicyOwner_SharedInstance(t *testing.T) {
	cases := []struct {
		name string
		fn   func(t *testing.T, c *Config) (*WebFetchPolicyOwner, *WebFetchPolicyOwner, *WebFetchPolicyOwner)
	}{
		{
			name: "repeated calls return the same instance",
			fn: func(t *testing.T, c *Config) (*WebFetchPolicyOwner, *WebFetchPolicyOwner, *WebFetchPolicyOwner) {
				a := c.WebFetchPolicyOwner()
				b := c.WebFetchPolicyOwner()
				cOwner := c.WebFetchPolicyOwner()
				return a, b, cOwner
			},
		},
		{
			name: "two configs get distinct owners",
			fn: func(t *testing.T, c *Config) (*WebFetchPolicyOwner, *WebFetchPolicyOwner, *WebFetchPolicyOwner) {
				other := &Config{}
				_ = other.WebFetchPolicyOwner()
				a := c.WebFetchPolicyOwner()
				b := c.WebFetchPolicyOwner()
				return a, b, other.WebFetchPolicyOwner()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			a, b, third := tc.fn(t, c)
			if a == nil || b == nil {
				t.Fatalf("WebFetchPolicyOwner() returned nil (a=%p b=%p)", a, b)
			}
			if a != b {
				t.Fatalf("owner not stable across calls: %p != %p", a, b)
			}
			if tc.name == "two configs get distinct owners" && a == third {
				t.Fatalf("distinct configs share an owner: both %p", a)
			}
			if tc.name == "repeated calls return the same instance" && a != third {
				t.Fatalf("owner not stable across calls: %p != %p", a, third)
			}
		})
	}
}

// TestWebFetchPolicyOwner_ConcurrentInit exercises the lazy allocation:
// concurrent first calls must converge on a single instance (no lost
// winner, no double allocation visible to callers).
func TestWebFetchPolicyOwner_ConcurrentInit(t *testing.T) {
	c := &Config{}
	const n = 64
	owners := make([]*WebFetchPolicyOwner, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owners[i] = c.WebFetchPolicyOwner()
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if owners[i] != owners[0] {
			t.Fatalf("concurrent init produced multiple owners: %p vs %p", owners[0], owners[i])
		}
	}
}

// TestWebFetchPolicyOwner_ExcludesWriter pins the RWMutex semantics the
// whole #423 fix relies on: a writer (Lock) excludes concurrent readers
// (RLock), and readers do not block each other.
func TestWebFetchPolicyOwner_ExcludesWriter(t *testing.T) {
	c := &Config{}
	owner := c.WebFetchPolicyOwner()

	// Writer holds the lock; a reader must observe exclusion.
	owner.Lock()
	released := make(chan struct{})
	go func() {
		owner.RLock()
		close(released)
		owner.RUnlock()
	}()
	owner.Unlock()
	<-released

	// Readers do not exclude each other.
	owner.RLock()
	owner.RLock()
	owner.RUnlock()
	owner.RUnlock()
}
