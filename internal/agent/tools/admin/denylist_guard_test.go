package admin

import (
	"bytes"
	"context"
	"log"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestDenylistMu_NilGuard pins review finding #3 (nil-mutex fragility): a
// denylist handler running with URLDenylistMu unwired must surface the
// misconfiguration prominently AND receive a safe, non-nil mutex instead of
// a bare nil, so handlers that omit a nil-check before Lock/RLock cannot
// nil-panic. The no-op mutex is only legal in single-threaded
// standalone/test Deps.
func TestDenylistMu_NilGuard(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	// Unwired Deps: guard returns a STABLE NO-OP mutex (never nil) AND
	// logs the warning. Repeated calls must return the same instance so
	// the lock is a real shared mutex rather than a fresh one per call.
	if mu := (Deps{}).denylistMu(); mu == nil {
		t.Fatalf("denylistMu() = nil for unwired Deps, want a stable no-op mutex (never nil)")
	}
	if got, again := (Deps{}).denylistMu(), (Deps{}).denylistMu(); got != again {
		t.Fatalf("denylistMu() on unwired Deps returned different instances (%p vs %p), want the shared no-op mutex", got, again)
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

// TestDenylistHandlers_UnwiredNoPanic pins the robustness half of review
// finding #3: with Deps.URLDenylistMu nil (unwired), every denylist handler
// must run to completion without nil-panicking on the mutex it obtains from
// denylistMu(). A real DB is wired so logAudit's deps.DB.LogAudit is
// exercised too; Cfg is a fresh config with an empty WebFetch denylist.
func TestDenylistHandlers_UnwiredNoPanic(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// URLDenylistMu intentionally left nil (unwired).
	deps := Deps{
		DB:      db,
		Actor:   "parent",
		Gateway: "test",
		Cfg:     &config.Config{Users: []config.UserConfig{{Name: "parent", Role: "parent"}}},
	}

	ctx := context.Background()

	// list: read path.
	if _, err := HandleDenylistList(ctx, deps, map[string]any{}); err != nil {
		t.Fatalf("HandleDenylistList unwired: %v", err)
	}

	// add: exclusive read-modify-write + audit.
	out, err := HandleDenylistAdd(ctx, deps, map[string]any{"host": "example.com"})
	if err != nil {
		t.Fatalf("HandleDenylistAdd unwired: %v", err)
	}
	if len(deps.Cfg.Tools.WebFetch.URLDenylist) != 1 || deps.Cfg.Tools.WebFetch.URLDenylist[0] != "example.com" {
		t.Fatalf("HandleDenylistAdd unwired did not append host, got: %v (out=%q)", deps.Cfg.Tools.WebFetch.URLDenylist, out)
	}

	// remove: exclusive read-modify-write + audit.
	if _, err := HandleDenylistRemove(ctx, deps, map[string]any{"host": "example.com"}); err != nil {
		t.Fatalf("HandleDenylistRemove unwired: %v", err)
	}
	if len(deps.Cfg.Tools.WebFetch.URLDenylist) != 0 {
		t.Fatalf("HandleDenylistRemove unwired did not clear the list, got: %v", deps.Cfg.Tools.WebFetch.URLDenylist)
	}
}
