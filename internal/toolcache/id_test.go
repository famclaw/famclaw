package toolcache

import (
	"testing"
	"time"
)

func TestNewIDLengthAndCharset(t *testing.T) {
	id := NewID()
	if len(id) != 26 {
		t.Errorf("id length = %d, want 26", len(id))
	}
	for _, r := range id {
		ok := (r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z' && r != 'I' && r != 'L' && r != 'O' && r != 'U')
		if !ok {
			t.Errorf("invalid char %q in id %s", r, id)
		}
	}
}

func TestNewIDLexicographicallyTimeOrdered(t *testing.T) {
	a := NewID()
	time.Sleep(2 * time.Millisecond)
	b := NewID()
	if a >= b {
		t.Errorf("expected lex ordering: %s should sort before %s", a, b)
	}
}

func TestNewIDsUniqueWhenMintedRapidly(t *testing.T) {
	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id after %d mints: %s", i, id)
		}
		seen[id] = true
	}
}

func TestNewIDsMonotonicWithinSameMillisecond(t *testing.T) {
	// Mint a burst as fast as Go will let us; verify each id > previous
	// (monotonic random bump within same ms).
	prev := NewID()
	for i := 0; i < 100; i++ {
		next := NewID()
		if next <= prev {
			t.Errorf("non-monotonic: %s should be > %s", next, prev)
		}
		prev = next
	}
}
