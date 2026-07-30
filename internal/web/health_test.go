package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// /api/health surfaces MCP servers skipped at boot. Previously initMCPPool computed
// that list and main.go discarded it, so a misconfigured MCP server printed one line
// at startup and was invisible to operators thereafter.

func newHealthTestServer(skipped []string) *Server {
	s := &Server{cfg: &config.Config{}, cfgMu: sync.RWMutex{}}
	s.SetMCPSkipped(skipped)
	return s
}

func getHealthBody(t *testing.T, s *Server) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	return rec.Code, rec.Body.String()
}

func TestHealth_ReportsSkippedMCPServers(t *testing.T) {
	code, body := getHealthBody(t, newHealthTestServer([]string{
		"inventory (failed to start)",
		"calendar (sandbox enabled but failed to start)",
	}))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{"inventory (failed to start)", "calendar (sandbox enabled but failed to start)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody: %s", want, body)
		}
	}
}

// Pins the empty case as [] and never null. This MUST assert on RAW JSON:
// json.Unmarshal collapses null and [] into the same nil slice, so an
// unmarshalling test cannot tell them apart - which is the whole point here.
// A null forces every consumer to nil-check a field that should always be a list.
func TestHealth_EmptySkipListIsArrayNotNull(t *testing.T) {
	_, body := getHealthBody(t, newHealthTestServer(nil))
	if !strings.Contains(body, `"mcp_skipped":[]`) {
		t.Errorf("raw body must contain \"mcp_skipped\":[] and never null\nbody: %s", body)
	}
	if strings.Contains(body, `"mcp_skipped":null`) {
		t.Errorf("mcp_skipped serialised as null\nbody: %s", body)
	}
}

func TestHealth_KeepsExistingKeys(t *testing.T) {
	_, body := getHealthBody(t, newHealthTestServer(nil))
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	for _, k := range []string{"status", "version", "time", "needs_setup", "mcp_skipped"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q\nbody: %s", k, body)
		}
	}
}
