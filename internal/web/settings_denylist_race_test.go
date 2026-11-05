package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/agent"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// TestSettingsPost_DenylistConcurrentWithHostDecision pins the unified
// denylist lock (PR #423 current-head finding, review 5788420595): the
// /api/settings POST replaces cfg.Tools.WebFetch.URLDenylist while the
// agent's host gate reads the same slice. Before the fix the writer held
// only Server.cfgMu and the reader only the agent's per-instance mutex —
// two independent locks guarding one slice, so the concurrent
// slice-header write/read below is a data race (and two concurrent chat
// turns would race even the reader-vs-admin-writer pair, since agents are
// constructed per turn). After the fix both sides take the shared lock
// owned by the *Config they both hold (config.WebFetchDenylistMu; lock
// order cfgMu → WebFetchDenylistMu, documented on that field). This test
// only has teeth under -race; run with
// `go test -race ./internal/web/...`.
func TestSettingsPost_DenylistConcurrentWithHostDecision(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", Role: "parent", PIN: "1234"},
			{Name: "teo", Role: "child", AgeGroup: "age_5_12"},
		},
		Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{
			Enabled:     true,
			URLPolicy:   "denylist",
			URLDenylist: []string{"seed.example"},
		}},
	}

	// handleSettingsPost persists the whole config to cfgPath after a
	// successful save — pre-create the target (same pattern as
	// TestSettingsPost_AcceptsAfterAuth).
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed config file: %v", err)
	}

	s := &Server{cfg: cfg, cfgPath: cfgPath, cfgMu: sync.RWMutex{}}

	// A real agent sharing the SAME *config.Config — the production
	// shape: main.go hands the loaded config to both the web server and
	// every per-turn agent. The in-memory db matches
	// TestHostDecision_DenylistConcurrentReadWrite's setup so the reader
	// path stays safe if hostDecision's private-IP short-circuit is ever
	// refactored to consult approvals first.
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	child := &config.UserConfig{Name: "teo", Role: "child", AgeGroup: "age_5_12"}
	a, err := agent.NewAgent(child, cfg, nil, nil, nil, db, agent.AgentDeps{})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	// Reader host: a private IP short-circuits every gate after the
	// WebFetch snapshot + denylist read in hostDecision (no db, no
	// detector), so the only shared state the reader path touches under
	// the lock is the very slice the writer churns (same premise as
	// TestHostDecision_DenylistConcurrentReadWrite in internal/agent).
	const host = "192.168.1.50"
	const cycles = 50
	var wg sync.WaitGroup

	// Writer: the real settings POST path. Every cycle sends an explicit
	// url_denylist, so the handler builds and installs a NEW slice header
	// each time — the write that races the reader's snapshot.
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := `{"web_fetch":{"enabled":true,"url_denylist":["churn.example","seed.example"]}}`
		for i := 0; i < cycles; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			s.handleSettingsPost(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("handleSettingsPost cycle %d: status = %d, body = %s", i, rec.Code, rec.Body.String())
				return
			}
		}
	}()

	// Reader: the agent's host gate against the same shared cfg.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < cycles*10; i++ {
			if ok, reason := a.HostDecision(context.Background(), host); !ok || reason != "" {
				t.Errorf("HostDecision(%q) = (%v, %q) during concurrent settings churn; want (true, \"\")", host, ok, reason)
				return
			}
		}
	}()

	wg.Wait()

	// Settled state: the GET view must show the last POST's list.
	rec := httptest.NewRecorder()
	s.handleSettingsGet(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handleSettingsGet: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var view settingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal GET response: %v", err)
	}
	want := []string{"churn.example", "seed.example"}
	if !reflect.DeepEqual(view.WebFetch.URLDenylist, want) {
		t.Fatalf("settled URLDenylist = %v, want %v", view.WebFetch.URLDenylist, want)
	}
}
