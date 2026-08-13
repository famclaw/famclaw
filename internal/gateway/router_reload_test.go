package gateway

import (
	"errors"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/reload"
)

// failingReloader is a reload.Reloader that always returns an error,
// simulating a component whose config cannot be applied at runtime.
type failingReloader struct{}

func (f *failingReloader) ReloadConfig(*config.Config) error {
	return errors.New("simulated reload failure")
}

// makeReloadConfig returns a minimal config valid for reload tests, with the
// given Server.Secret so tests can observe that a reload actually took effect.
func makeReloadConfig(t *testing.T, secret string) *config.Config {
	t.Helper()
	return &config.Config{
		Server: config.ServerConfig{
			Host:   "localhost",
			Port:   8080,
			Secret: secret,
		},
		LLM: config.LLMConfig{
			Temperature:       0.7,
			MaxResponseTokens: 512,
		},
		Users: []config.UserConfig{
			{Name: "parent", DisplayName: "Parent", Role: "parent", PIN: "1234"},
			{Name: "emma", DisplayName: "Emma", Role: "child", AgeGroup: "age_8_12"},
		},
	}
}

// cfgSecret returns the router's current Server.Secret, reading under the
// config mutex so the access is safe against concurrent reload.
func cfgSecret(r *Router) string {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.cfg.Server.Secret
}

// TestRouterUpdateConfig verifies that UpdateConfig replaces the router's
// configuration so that subsequent operations observe the new values.
func TestRouterUpdateConfig(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	original := cfgSecret(router)
	if original != "test-secret" {
		t.Fatalf("setup secret = %q, want %q", original, "test-secret")
	}

	newCfg := makeReloadConfig(t, "updated-secret")
	router.UpdateConfig(newCfg)

	if got := cfgSecret(router); got != "updated-secret" {
		t.Errorf("after UpdateConfig, Server.Secret = %q, want %q", got, "updated-secret")
	}
}

// TestRouterReloadConfig verifies that ReloadConfig (the reload.Reloader
// implementation the PR adds) applies the new config and returns nil — the
// genuinely-reloadable path that must not be falsely reported as failed.
func TestRouterReloadConfig(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	newCfg := makeReloadConfig(t, "reloaded-secret")
	err := router.ReloadConfig(newCfg)

	if err != nil {
		t.Fatalf("ReloadConfig returned error: %v", err)
	}

	if got := cfgSecret(router); got != "reloaded-secret" {
		t.Errorf("after ReloadConfig, Server.Secret = %q, want %q", got, "reloaded-secret")
	}
}

// TestRouterReloadThroughRegistry verifies the full PR #326 contract: the
// registry walks every component and reports per-component truth — the router
// (genuinely reloadable) reports success, a component that errors reports
// failure honestly, and a non-reloadable component is reported as requiring
// a restart rather than being silently skipped.
func TestRouterReloadThroughRegistry(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	reg := reload.NewRegistry()
	reg.Register("router", router)
	reg.Register("broken-component", &failingReloader{})
	reg.RequireRestart("gateway-bots", "bots are started once at startup")

	newCfg := makeReloadConfig(t, "registry-secret")
	statuses := reg.Reload(newCfg)

	cases := []struct {
		name, wantOutcome, wantDetail string
	}{
		{"router", string(reload.OutcomeReloaded), ""},
		{"broken-component", string(reload.OutcomeFailed), "simulated reload failure"},
		{"gateway-bots", string(reload.OutcomeRequiresRestart), "bots are started once at startup"},
	}

	if len(statuses) != len(cases) {
		t.Fatalf("expected %d statuses, got %d", len(cases), len(statuses))
	}

	byName := make(map[string]reload.Status, len(statuses))
	for _, s := range statuses {
		byName[s.Name] = s
	}

	for _, tc := range cases {
		s, ok := byName[tc.name]
		if !ok {
			t.Errorf("missing status for %q", tc.name)
			continue
		}
		if string(s.Outcome) != tc.wantOutcome {
			t.Errorf("%q outcome = %q, want %q", tc.name, s.Outcome, tc.wantOutcome)
		}
		if s.Detail != tc.wantDetail {
			t.Errorf("%q detail = %q, want %q", tc.name, s.Detail, tc.wantDetail)
		}
	}

	// The router must have actually applied the new config — not merely
	// reported success to the registry.
	if got := cfgSecret(router); got != "registry-secret" {
		t.Errorf("after registry reload, router config Server.Secret = %q, want %q",
			got, "registry-secret")
	}
}

// TestRouterReloadConfigConcurrentSafe verifies that ReloadConfig can be
// called concurrently (the running server may handle messages while a
// reload is in flight) without racing or corrupting the config pointer.
// The cfgMutex makes the swap atomic, so the final secret must be one of
// the values written.
func TestRouterReloadConfigConcurrentSafe(t *testing.T) {
	router, _ := setupRouter(t, echoChat)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := makeReloadConfig(t, "secret-"+string(rune('a'+i)))
			if err := router.ReloadConfig(cfg); err != nil {
				t.Errorf("ReloadConfig error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got := cfgSecret(router)
	switch got {
	case "secret-a", "secret-b", "secret-c", "secret-d":
		// ok
	default:
		t.Errorf("after concurrent reloads, Server.Secret = %q, want one of the reloaded values", got)
	}
}
