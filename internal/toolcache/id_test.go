package toolcache

import (
	"testing"
	"time"
)

func mustGenerate(t *testing.T, g *IDGenerator) string {
	t.Helper()
	id, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return id
}

func TestNewIDLengthAndCharset(t *testing.T) {
	g := NewIDGenerator()
	id := mustGenerate(t, g)
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
	g := NewIDGenerator()
	a := mustGenerate(t, g)
	time.Sleep(2 * time.Millisecond)
	b := mustGenerate(t, g)
	if a >= b {
		t.Errorf("expected lex ordering: %s should sort before %s", a, b)
	}
}

func TestNewIDsUniqueWhenMintedRapidly(t *testing.T) {
	g := NewIDGenerator()
	const n = 1000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := mustGenerate(t, g)
		if seen[id] {
			t.Fatalf("duplicate id after %d mints: %s", i, id)
		}
		seen[id] = true
	}
}

func TestNewIDsMonotonicWithinSameMillisecond(t *testing.T) {
	// Mint a burst as fast as Go will let us; verify each id > previous
	// (monotonic random bump within same ms).
	g := NewIDGenerator()
	prev := mustGenerate(t, g)
	for i := 0; i < 100; i++ {
		next := mustGenerate(t, g)
		if next <= prev {
			t.Errorf("non-monotonic: %s should be > %s", next, prev)
		}
		prev = next
	}
}

func TestIDGeneratorInstancesAreIndependent(t *testing.T) {
	// Two generators should mint distinct ID sequences from independent
	// state — no cross-instance leakage of lastMillis/lastRand.
	g1 := NewIDGenerator()
	g2 := NewIDGenerator()
	seen := make(map[string]bool, 200)
	for i := 0; i < 100; i++ {
		for _, id := range []string{mustGenerate(t, g1), mustGenerate(t, g2)} {
			if seen[id] {
				t.Fatalf("collision across generators: %s", id)
			}
			seen[id] = true
		}
	}
}
