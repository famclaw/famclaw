package reload

import (
	"errors"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// fakeReloader is a test Reloader whose behaviour is configurable.
type fakeReloader struct {
	calls     int
	err       error
	gotCfg    *config.Config
	returnErr bool
}

func (f *fakeReloader) ReloadConfig(cfg *config.Config) error {
	f.calls++
	f.gotCfg = cfg
	if f.returnErr {
		return errors.New("reload failed")
	}
	return nil
}

// newCfg builds a minimal *config.Config for tests.
func newCfg() *config.Config {
	return &config.Config{}
}

func TestRegistry_EmptyReload(t *testing.T) {
	r := NewRegistry()
	statuses := r.Reload(newCfg())
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestRegistry_RegisterAndReload(t *testing.T) {
	r := NewRegistry()
	fr := &fakeReloader{}
	r.Register("component-a", fr)

	statuses := r.Reload(newCfg())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Name != "component-a" {
		t.Errorf("name = %q, want component-a", statuses[0].Name)
	}
	if statuses[0].Outcome != OutcomeReloaded {
		t.Errorf("outcome = %q, want %q", statuses[0].Outcome, OutcomeReloaded)
	}
	if fr.calls != 1 {
		t.Errorf("expected 1 call, got %d", fr.calls)
	}
	if fr.gotCfg == nil {
		t.Error("expected config to be passed to Reloader")
	}
}

func TestRegistry_FailedReloader(t *testing.T) {
	r := NewRegistry()
	fr := &fakeReloader{returnErr: true}
	r.Register("broken-component", fr)

	statuses := r.Reload(newCfg())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Outcome != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", statuses[0].Outcome, OutcomeFailed)
	}
	if statuses[0].Detail == "" {
		t.Error("expected non-empty detail for failed reload")
	}
}

func TestRegistry_RequiresRestart(t *testing.T) {
	r := NewRegistry()
	r.RequireRestart("database", "connection opened once at startup")

	statuses := r.Reload(newCfg())
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Name != "database" {
		t.Errorf("name = %q, want database", statuses[0].Name)
	}
	if statuses[0].Outcome != OutcomeRequiresRestart {
		t.Errorf("outcome = %q, want %q", statuses[0].Outcome, OutcomeRequiresRestart)
	}
	if statuses[0].Detail != "connection opened once at startup" {
		t.Errorf("detail = %q, want full reason", statuses[0].Detail)
	}
}

func TestRegistry_MixedOutcomes(t *testing.T) {
	r := NewRegistry()
	okReloader := &fakeReloader{}
	fr := &fakeReloader{returnErr: true}

	r.Register("router", okReloader)
	r.Register("broken", fr)
	r.RequireRestart("gateway", "bots are started once at startup")

	statuses := r.Reload(newCfg())
	if len(statuses) != 3 {
		t.Fatalf("expected 3 statuses, got %d", len(statuses))
	}

	walk := func(idx int, wantName, wantOutcome string) {
		t.Helper()
		s := statuses[idx]
		if s.Name != wantName {
			t.Errorf("[%d] name = %q, want %q", idx, s.Name, wantName)
		}
		if string(s.Outcome) != wantOutcome {
			t.Errorf("[%d] outcome = %q, want %q", idx, s.Outcome, wantOutcome)
		}
	}

	walk(0, "router", string(OutcomeReloaded))
	walk(1, "broken", string(OutcomeFailed))
	walk(2, "gateway", string(OutcomeRequiresRestart))
}

func TestRegistry_Count(t *testing.T) {
	r := NewRegistry()
	r.Register("a", &fakeReloader{})
	r.Register("b", &fakeReloader{})
	r.RequireRestart("c", "reason")

	if c := r.Count(); c != 3 {
		t.Errorf("Count = %d, want 3", c)
	}
}

// TestReloader_PassesConfig verifies that the exact config pointer passed
// to Reload is the same one each Reloader receives — so a config change
// is observable downstream.
func TestReloader_PassesConfig(t *testing.T) {
	r := NewRegistry()
	fr := &fakeReloader{}
	r.Register("transcriber", fr)

	cfg := &config.Config{Server: config.ServerConfig{Secret: "test-key"}}
	r.Reload(cfg)

	if fr.gotCfg != cfg {
		t.Error("Reloader did not receive the exact config pointer")
	}
}
