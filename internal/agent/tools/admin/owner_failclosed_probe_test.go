package admin

import (
	"context"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestAllowlistFailsClosedWhenOwnerUnwired is the RED/GREEN probe for the
// shared-policy-owner port (#423 round 18).
//
// It uses only the Deps fields that exist in BOTH generations (DB, Actor,
// Gateway, Cfg, ConfigPath), so the same source compiles on the #423 base
// and on the owner-ported tree:
//
//   - On the #423 base the allowlist handlers took NO lock at all and
//     rewrote Cfg.Tools.WebFetch.URLAllowlist unsynchronized. An unwired
//     handler SUCCEEDED, so this assertion is RED.
//   - After the owner port, an allowlist handler with no shared policy
//     owner available must FAIL CLOSED (ErrPolicyOwnerUnwired) instead of
//     touching the list, so this assertion is GREEN.
func TestAllowlistFailsClosedWhenOwnerUnwired(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// No policy lock wired in either generation. On the base this leaves
	// URLDenylistMu nil; on the owner-ported tree it leaves
	// SharedPolicyOwner nil. The literal names only fields present in both.
	deps := Deps{
		DB:         db,
		Actor:      "parent",
		Gateway:    "test",
		ConfigPath: "",
		Cfg: &config.Config{
			Users: []config.UserConfig{{Name: "parent", Role: "parent"}},
		},
	}

	if out, err := HandleAllowlistAdd(context.Background(), deps, map[string]any{"host": "example.com"}); err == nil {
		t.Fatalf("allowlist_add with an unwired owner SUCCEEDED (out=%q) — the handler rewrote the allowlist unsynchronized; want a fail-closed error", out)
	}
}
