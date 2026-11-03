package admin

import (
	"bytes"
	"log"
	"sync"
	"testing"
)

// TestDenylistMu_NilGuard pins review finding #3 (nil-mutex race): a
// denylist handler running with URLDenylistMu unwired must surface the
// misconfiguration prominently instead of silently falling back to an
// unsynchronized read-modify-write. Nil is only legal in
// single-threaded standalone/test Deps.
func TestDenylistMu_NilGuard(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	// Unwired Deps: guard returns nil AND logs the warning.
	if mu := (Deps{}).denylistMu(); mu != nil {
		t.Fatalf("denylistMu() = %v, want nil for unwired Deps", mu)
	}
	if !bytes.Contains(buf.Bytes(), []byte("URLDenylistMu unset")) ||
		!bytes.Contains(buf.Bytes(), []byte("WARNING")) {
		t.Fatalf("denylistMu() on unwired Deps did not log the expected warning, got: %q", buf.String())
	}

	// Wired Deps: guard returns the exact shared instance, no warning.
	var mu sync.RWMutex
	buf.Reset()
	if got := (Deps{URLDenylistMu: &mu}).denylistMu(); got != &mu {
		t.Fatalf("denylistMu() = %v, want the shared instance", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("denylistMu() on wired Deps logged unexpectedly: %q", buf.String())
	}
}
