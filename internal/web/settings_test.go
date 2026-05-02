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

// TestSettingsPost_PINHandling locks in the contract for handleSettingsPost
// across the four PIN scenarios that #109 broke:
//   1. true first boot (no parent users) → no PIN required
//   2. re-run with correct PIN → accepted
//   3. re-run with wrong PIN → rejected
//   4. re-run with no PIN → rejected
//
// Without these tests the wizard-rerun-with-PIN regression could quietly
// reappear: handleSettingsPost gates on isFirstBoot(), and any change to
// that helper risks reintroducing the 403-on-rerun bug.
func TestSettingsPost_PINHandling(t *testing.T) {
	parent := config.UserConfig{
		Name:        "sarah",
		DisplayName: "Sarah",
		Role:        "parent",
		PIN:         "1234",
	}

	cases := []struct {
		name       string
		users      []config.UserConfig
		pinHeader  string
		setHeader  bool
		wantStatus int
	}{
		{
			name:       "first boot no PIN accepted",
			users:      nil,
			setHeader:  false,
			wantStatus: http.StatusOK,
		},
		{
			name:       "wizard rerun with correct PIN accepted",
			users:      []config.UserConfig{parent},
			pinHeader:  "1234",
			setHeader:  true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "wizard rerun with wrong PIN rejected",
			users:      []config.UserConfig{parent},
			pinHeader:  "9999",
			setHeader:  true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wizard rerun with no PIN rejected",
			users:      []config.UserConfig{parent},
			setHeader:  false,
			wantStatus: http.StatusForbidden,
		},
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

			// Empty settingsView body — exercises only the PIN gate, not
			// the field merging path.
			req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte("{}")))
			if tc.setHeader {
				req.Header.Set("X-Parent-PIN", tc.pinHeader)
			}
			rec := httptest.NewRecorder()

			s.handleSettingsPost(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
