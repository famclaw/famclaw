package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// TestSettingsPost_RollbackConcurrentWithWebSearchRead pins the #423
// followup-2 finding (settings.go, PM-confirmed at 87f6d612): on a failed
// writeConfig the settings POST rolled back with a WHOLE-STRUCT write
// (`s.cfg.Tools = snapTools`) that overwrote every Tools sub-field,
// including WebSearch. The agent's handleWebSearch read
// (`cfg := a.cfg.Tools.WebSearch`, agent.go:1993) takes no lock — it runs
// on every per-turn agent — so that rollback write races a live reader: a
// benign-in-value data race, still a memory-model violation. The fix
// narrows the snapshot and rollback to the one section this handler
// owns (WebFetch): `s.cfg.Tools.WebFetch = snapWebFetch`.
//
// The writer goroutine drives the REAL handleSettingsPost path and forces
// the writeConfig failure (cfgPath in a nonexistent directory → CreateTemp
// fails) so every cycle executes the rollback. The reader goroutine does
// the exact production read shape: a lock-free value copy of
// cfg.Tools.WebSearch, field-touched so the copy cannot be elided.
//
// At the pre-fix whole-struct rollback this trips `go test -race`
// (write of the WebSearch words in `s.cfg.Tools = snapTools` vs the
// reader's copy); after the scoped-WebFetch fix nothing in this handler
// ever writes WebSearch, so the run is clean. Only has teeth under -race;
// run with `go test -race ./internal/web/...`.
func TestSettingsPost_RollbackConcurrentWithWebSearchRead(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", Role: "parent", PIN: "1234"},
			{Name: "teo", Role: "child", AgeGroup: "age_5_12"},
		},
		Tools: config.ToolsConfig{
			WebFetch: config.WebFetchConfig{
				Enabled:     true,
				URLPolicy:   "denylist",
				URLDenylist: []string{"seed.example"},
			},
			// Distinctive values: this is the section the settings
			// handler must never touch — only web_search (agent.go)
			// reads it at runtime, lock-free.
			WebSearch: config.WebSearchConfig{
				Enabled:      true,
				Endpoint:     "http://localhost:8888",
				MaxResults:   12,
				TimeoutSec:   45,
				AllowedRoles: []string{"parent", "child"},
			},
		},
	}
	orig := cfg.Tools

	// Force the writeConfig FAILURE path on every cycle: the target
	// directory does not exist, so os.CreateTemp inside writeConfig
	// fails and handleSettingsPost executes its rollback.
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "missing-dir", "config.yaml")

	s := &Server{cfg: cfg, cfgPath: cfgPath, cfgMu: sync.RWMutex{}}

	const cycles = 200
	var wg sync.WaitGroup

	// Writer: the real settings POST, always landing in the rollback.
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := `{"web_fetch":{"enabled":true,"url_denylist":["churn.example","seed.example"]}}`
		for i := 0; i < cycles; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			s.handleSettingsPost(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("cycle %d: status = %d, body = %s; want 500 (writeConfig must fail so the rollback path runs)", i, rec.Code, rec.Body.String())
				return
			}
			if !strings.Contains(rec.Body.String(), "saving config") {
				t.Errorf("cycle %d: error = %q; want the writeConfig failure, not a validation rejection", i, rec.Body.String())
				return
			}
		}
	}()

	// Reader: the production lock-free read shape from
	// handleWebSearch (agent.go:1993) — `cfg := a.cfg.Tools.WebSearch`
	// is a plain value copy taken without any lock, on every chat turn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < cycles*10; i++ {
			ws := cfg.Tools.WebSearch
			// Touch the copied words so the read is observable and
			// cannot be elided.
			_ = ws.Endpoint
			_ = ws.AllowedRoles
			_ = ws.MaxResults
			_ = ws.TimeoutSec
		}
	}()

	wg.Wait()

	// Settled state: every cycle rolled back, so the Tools section must
	// be byte-identical to the pre-POST value (WebFetch included — the
	// denylist churn never persisted because every write failed).
	if !reflect.DeepEqual(cfg.Tools, orig) {
		t.Fatalf("settled cfg.Tools = %+v, want the unchanged pre-POST %+v", cfg.Tools, orig)
	}
}
