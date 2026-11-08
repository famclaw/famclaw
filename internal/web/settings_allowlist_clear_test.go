package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
)

func TestSettingsPostAllowlistPresence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		mode, body string
		status     int
		want       []string
	}{
		{"absent preserves", true, "denylist", `{"web_fetch":{"enabled":true,"url_policy":"denylist"}}`, http.StatusOK, []string{"kept.example"}},
		{"disabled clears", false, "allowlist", `{"web_fetch":{"enabled":false,"url_policy":"allowlist","url_allowlist":[]}}`, http.StatusOK, []string{}},
		{"denylist clears", true, "denylist", `{"web_fetch":{"enabled":true,"url_policy":"denylist","url_allowlist":[]}}`, http.StatusOK, []string{}},
		{"enabled allowlist rejects empty", true, "allowlist", `{"web_fetch":{"enabled":true,"url_policy":"allowlist","url_allowlist":[]},"llm":{"base_url":"https://changed.example","model":"changed"}}`, http.StatusBadRequest, []string{"kept.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			cfg := &config.Config{LLM: config.LLMConfig{BaseURL: "https://original.example", Model: "original"}, Users: []config.UserConfig{{Name: "parent", Role: "parent", PIN: "1234"}}, Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: tc.enabled, URLPolicy: tc.mode, URLAllowlist: []string{"kept.example"}}}}
			before, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			s := &Server{cfg: cfg, cfgPath: path}
			rec := httptest.NewRecorder()
			s.handleSettingsPost(rec, httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(tc.body)))
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if !reflect.DeepEqual(cfg.Tools.WebFetch.URLAllowlist, tc.want) {
				t.Fatalf("memory allowlist=%v want=%v", cfg.Tools.WebFetch.URLAllowlist, tc.want)
			}
			disk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != http.StatusOK {
				if !bytes.Equal(disk, before) || cfg.LLM.BaseURL != "https://original.example" {
					t.Fatal("rejected update changed memory or file")
				}
				return
			}
			var persisted config.Config
			if err := yaml.Unmarshal(disk, &persisted); err != nil {
				t.Fatal(err)
			}
			if len(persisted.Tools.WebFetch.URLAllowlist) != len(tc.want) {
				t.Fatalf("persisted allowlist=%v want=%v", persisted.Tools.WebFetch.URLAllowlist, tc.want)
			}
			if len(tc.want) > 0 && !reflect.DeepEqual(persisted.Tools.WebFetch.URLAllowlist, tc.want) {
				t.Fatalf("persisted allowlist=%v want=%v", persisted.Tools.WebFetch.URLAllowlist, tc.want)
			}
		})
	}
}
