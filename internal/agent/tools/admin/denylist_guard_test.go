package admin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestPolicyOwner_NilGuard pins the fail-closed wiring contract (#423 round
// 18 successor of review finding #2): a web-policy handler running with
// SharedPolicyOwner unwired must fail closed — policyOwner() returns
// ErrPolicyOwnerUnwired so the handler surfaces the misconfiguration at the
// call site instead of touching the config's policy lists unsynchronized.
func TestPolicyOwner_NilGuard(t *testing.T) {
	// Unwired Deps: error + nil owner — the handler fails closed and
	// never takes a partial lock.
	unwired, err := (Deps{}).policyOwner()
	if !errors.Is(err, ErrPolicyOwnerUnwired) {
		t.Fatalf("policyOwner() on unwired Deps: err = %v, want ErrPolicyOwnerUnwired", err)
	}
	if unwired != nil {
		t.Fatalf("policyOwner() on unwired Deps returned owner %p, want nil", unwired)
	}

	// Wired Deps: the exact shared instance, no error — and two Deps
	// built from the same config reach the SAME owner (the #423 invariant
	// that the per-Agent value mutex could never provide).
	cfg := &config.Config{}
	got, err := (Deps{SharedPolicyOwner: cfg.WebFetchPolicyOwner()}).policyOwner()
	if err != nil {
		t.Fatalf("policyOwner() on wired Deps: err = %v, want nil", err)
	}
	if got != cfg.WebFetchPolicyOwner() {
		t.Fatalf("policyOwner() = %p, want the config's shared instance", got)
	}
}

// TestWebPolicyHandlers_UnwiredNoPanic pins the fail-closed contract of the
// shared owner: with Deps.SharedPolicyOwner nil (unwired), every web-policy
// handler — denylist AND allowlist (review F1: the allowlist handlers
// previously took no lock at all) — must return ErrPolicyOwnerUnwired, not
// panic, and must leave both lists untouched. The real DB is wired so a
// handler that slipped past the guard would be exercised end-to-end; the
// seed hosts prove the lists themselves were never mutated.
func TestWebPolicyHandlers_UnwiredNoPanic(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	seedDeny := []string{"deny-seed.example"}
	seedAllow := []string{"allow-seed.example"}

	// SharedPolicyOwner intentionally left nil (unwired).
	deps := Deps{
		DB:      db,
		Actor:   "parent",
		Gateway: "test",
		Cfg:     &config.Config{Users: []config.UserConfig{{Name: "parent", Role: "parent"}}},
	}
	deps.Cfg.Tools.WebFetch.URLDenylist = seedDeny
	deps.Cfg.Tools.WebFetch.URLAllowlist = seedAllow

	ctx := context.Background()

	cases := []struct {
		name string
		call func(ctx context.Context, deps Deps, args map[string]any) (string, error)
		args map[string]any
	}{
		{"denylist_list", HandleDenylistList, map[string]any{}},
		{"denylist_add", HandleDenylistAdd, map[string]any{"host": "example.com"}},
		{"denylist_remove", HandleDenylistRemove, map[string]any{"host": "deny-seed.example"}},
		{"allowlist_list", HandleAllowlistList, map[string]any{}},
		{"allowlist_add", HandleAllowlistAdd, map[string]any{"host": "example.com"}},
		{"allowlist_remove", HandleAllowlistRemove, map[string]any{"host": "allow-seed.example"}},
	}
	for _, tc := range cases {
		if out, err := tc.call(ctx, deps, tc.args); err == nil {
			t.Fatalf("%s unwired: err = nil, want ErrPolicyOwnerUnwired (out=%q)", tc.name, out)
		} else if !errors.Is(err, ErrPolicyOwnerUnwired) {
			t.Fatalf("%s unwired: err = %v, want ErrPolicyOwnerUnwired", tc.name, err)
		}
	}

	// No handler may have touched either list.
	if got := deps.Cfg.Tools.WebFetch.URLDenylist; !reflect.DeepEqual(got, seedDeny) {
		t.Fatalf("denylist after unwired handler calls = %v, want unmodified %v", got, seedDeny)
	}
	if got := deps.Cfg.Tools.WebFetch.URLAllowlist; !reflect.DeepEqual(got, seedAllow) {
		t.Fatalf("allowlist after unwired handler calls = %v, want unmodified %v", got, seedAllow)
	}
}
