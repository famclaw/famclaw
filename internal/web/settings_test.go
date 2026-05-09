package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

// TestSettingsPost_AcceptsAfterAuth verifies that handleSettingsPost itself
// is now auth-agnostic — the route is mounted behind s.protect(...) in
// Handler() and the handler trusts that gate. This test calls the handler
// directly (bypassing middleware) to lock in that the in-handler PIN check
// is gone; the middleware-level gate is covered separately.
func TestSettingsPost_AcceptsAfterAuth(t *testing.T) {
	parent := config.UserConfig{
		Name:        "sarah",
		DisplayName: "Sarah",
		Role:        "parent",
		PIN:         "1234",
	}

	cases := []struct {
		name  string
		users []config.UserConfig
	}{
		{name: "first boot no users", users: nil},
		{name: "post-bootstrap with parent", users: []config.UserConfig{parent}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			cfgPath := filepath.Join(tmp, "config.yaml")
			// handleSettingsPost writes the config back to cfgPath after a
			// successful save — pre-create an empty file so the write target
			// exists.
			if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o600); err != nil {
				t.Fatalf("seed config file: %v", err)
			}

			s := &Server{
				cfg:     &config.Config{Users: tc.users},
				cfgPath: cfgPath,
				cfgMu:   sync.RWMutex{},
			}

			req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte("{}")))
			rec := httptest.NewRecorder()

			s.handleSettingsPost(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}
