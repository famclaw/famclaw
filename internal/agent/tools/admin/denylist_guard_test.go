package admin

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestDenylistMu_NilGuard pins review finding #2 (nil-mutex fragility /
// global-state no-op lock): a denylist handler running with URLDenylistMu
// unwired must fail closed — denylistMu() returns ErrDenylistMuUnwired so
// the handler surfaces the misconfiguration at the call site instead of
// degrading to a lock that does not serialize against the agent's decision
// path.
func TestDenylistMu_NilGuard(t *testing.T) {
	// Unwired Deps: error + nil mutex — the handler fails closed and
	// never takes a partial lock.
	unwiredMu, err := (Deps{}).denylistMu()
	if !errors.Is(err, ErrDenylistMuUnwired) {
		t.Fatalf("denylistMu() on unwired Deps: err = %v, want ErrDenylistMuUnwired", err)
	}
	if unwiredMu != nil {
		t.Fatalf("denylistMu() on unwired Deps returned mutex %p, want nil", unwiredMu)
	}

	// Wired Deps: the exact shared instance, no error.
	var mu sync.RWMutex
	got, err := (Deps{URLDenylistMu: &mu}).denylistMu()
	if err != nil {
		t.Fatalf("denylistMu() on wired Deps: err = %v, want nil", err)
	}
	if got != &mu {
		t.Fatalf("denylistMu() = %p, want the shared instance", got)
	}
}

// TestDenylistHandlers_UnwiredNoPanic pins the fail-closed contract of
// finding #2: with Deps.URLDenylistMu nil (unwired), every denylist
// handler must return ErrDenylistMuUnwired — not panic on the mutex and
// not proceed with a partial lock — and must leave the denylist
// untouched. The real DB is wired so a handler that slipped past the
// guard would be exercised end-to-end; the seed host proves the list
// itself was never mutated.
func TestDenylistHandlers_UnwiredNoPanic(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	seed := []string{"seed.example"}

	// URLDenylistMu intentionally left nil (unwired).
	deps := Deps{
		DB:      db,
		Actor:   "parent",
		Gateway: "test",
		Cfg:     &config.Config{Users: []config.UserConfig{{Name: "parent", Role: "parent"}}},
	}
	deps.Cfg.Tools.WebFetch.URLDenylist = seed

	ctx := context.Background()

	// list: read path — must error, not panic.
	if _, err := HandleDenylistList(ctx, deps, map[string]any{}); !errors.Is(err, ErrDenylistMuUnwired) {
		t.Fatalf("HandleDenylistList unwired: err = %v, want ErrDenylistMuUnwired", err)
	}

	// add: must error and leave the list unmodified.
	if out, err := HandleDenylistAdd(ctx, deps, map[string]any{"host": "example.com"}); err == nil {
		t.Fatalf("HandleDenylistAdd unwired: err = nil, want ErrDenylistMuUnwired (out=%q)", out)
	} else if !errors.Is(err, ErrDenylistMuUnwired) {
		t.Fatalf("HandleDenylistAdd unwired: err = %v, want ErrDenylistMuUnwired", err)
	}

	// remove: must error and leave the list unmodified.
	if _, err := HandleDenylistRemove(ctx, deps, map[string]any{"host": "seed.example"}); !errors.Is(err, ErrDenylistMuUnwired) {
		t.Fatalf("HandleDenylistRemove unwired: err = %v, want ErrDenylistMuUnwired", err)
	}

	// No handler may have touched the denylist.
	if got := deps.Cfg.Tools.WebFetch.URLDenylist; !reflect.DeepEqual(got, seed) {
		t.Fatalf("denylist after unwired handler calls = %v, want unmodified %v", got, seed)
	}
}
