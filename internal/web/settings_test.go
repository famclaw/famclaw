package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"gopkg.in/yaml.v3"
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

// TestSettingsPost_RoundTrip verifies that a GET -> edit -> POST cycle
// preserves existing parent PINs and does not disclose them.
func TestSettingsPost_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "https://api.openai.com",
			Model:   "gpt-4",
		},
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
			{Name: "john", DisplayName: "John", Role: "parent", PIN: "5678"},
			{Name: "alice", DisplayName: "Alice", Role: "child"},
		},
		Gateways: config.GatewaysConfig{
			Telegram: config.TelegramConfig{Enabled: true, Token: "123456789:ABC"},
		},
		Tools: config.ToolsConfig{
			WebFetch: config.WebFetchConfig{
				Enabled:      true,
				URLAllowlist: []string{"example.com"},
			},
		},
	}

	data, _ := yaml.Marshal(initialCfg)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// Step 1: GET — verify PINs are NOT included in response
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettingsGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET failed: %d", rec.Code)
	}

	var response settingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal GET response: %v", err)
	}

	for _, user := range response.Users {
		if user.PIN != "" {
			t.Errorf("GET response leaks PIN for user %q: %q", user.Name, user.PIN)
		}
	}

	// Step 2: POST — send modified LLM, omit PINs (as GET did), update other fields
	updateBody := `{
		"llm": {
			"base_url": "https://api.anthropic.com",
			"model": "claude-3"
		},
		"users": [
			{"name": "sarah", "display_name": "Sarah Updated", "role": "parent"},
			{"name": "john", "display_name": "John Updated", "role": "parent"},
			{"name": "alice", "display_name": "Alice", "role": "child"}
		],
		"gateways": {
			"telegram": {"enabled": false, "token": ""},
			"discord": {"enabled": true, "token": "987654321:ZYX"}
		},
		"web_fetch": {
			"enabled": false,
			"url_allowlist": ["example.com", "test.com"]
		}
	}`

	req = httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(updateBody)))
	rec = httptest.NewRecorder()
	s.handleSettingsPost(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d (body: %s)", rec.Code, rec.Body.String())
	}

	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	// Verify LLM settings were updated
	if s.cfg.LLM.BaseURL != "https://api.anthropic.com" {
		t.Errorf("LLM.BaseURL = %q, want anthropic", s.cfg.LLM.BaseURL)
	}
	if s.cfg.LLM.Model != "claude-3" {
		t.Errorf("LLM.Model = %q, want claude-3", s.cfg.LLM.Model)
	}

	// Verify existing parent PINs are preserved despite being omitted in POST
	pinMap := make(map[string]string, len(s.cfg.Users))
	for _, u := range s.cfg.Users {
		pinMap[u.Name] = u.PIN
	}
	if pinMap["sarah"] != "1234" {
		t.Errorf("sarah PIN = %q, want 1234 (preserved from existing)", pinMap["sarah"])
	}
	if pinMap["john"] != "5678" {
		t.Errorf("john PIN = %q, want 5678 (preserved from existing)", pinMap["john"])
	}

	// Verify display names were updated
	for _, u := range s.cfg.Users {
		if u.Name == "sarah" && u.DisplayName != "Sarah Updated" {
			t.Errorf("sarah DisplayName = %q, want 'Sarah Updated'", u.DisplayName)
		}
		if u.Name == "john" && u.DisplayName != "John Updated" {
			t.Errorf("john DisplayName = %q, want 'John Updated'", u.DisplayName)
		}
	}

	// Verify gateways were updated
	if s.cfg.Gateways.Telegram.Enabled {
		t.Errorf("Telegram.Enabled = true, want false")
	}
	if !s.cfg.Gateways.Discord.Enabled {
		t.Errorf("Discord.Enabled = false, want true")
	}
	if s.cfg.Gateways.Discord.Token != "987654321:ZYX" {
		t.Errorf("Discord.Token = %q, want 987654321:ZYX", s.cfg.Gateways.Discord.Token)
	}

	// Verify web_fetch was updated
	if s.cfg.Tools.WebFetch.Enabled {
		t.Errorf("WebFetch.Enabled = true, want false")
	}
	if len(s.cfg.Tools.WebFetch.URLAllowlist) != 2 {
		t.Errorf("WebFetch.URLAllowlist has %d entries, want 2", len(s.cfg.Tools.WebFetch.URLAllowlist))
	}

	// Verify the persisted file contains the updated values
	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "https://api.anthropic.com") {
		t.Errorf("persisted file missing updated LLM base URL")
	}
}

// TestSettingsPost_ExplicitPinUpdate verifies that explicitly providing a
// new PIN updates it, while an omitted PIN preserves the existing value.
func TestSettingsPost_ExplicitPinUpdate(t *testing.T) {
	cases := []struct {
		name      string
		sarahPIN  string // POST value for sarah
		johnPIN   string // POST value for john (empty = preserve)
		wantSarah string // expected sarah PIN after POST
		wantJohn  string // expected john PIN after POST
	}{
		{
			name:      "update one, preserve the other",
			sarahPIN:  "9999",
			johnPIN:   "",
			wantSarah: "9999",
			wantJohn:  "5678",
		},
		{
			name:      "update both explicitly",
			sarahPIN:  "1111",
			johnPIN:   "2222",
			wantSarah: "1111",
			wantJohn:  "2222",
		},
		{
			name:      "preserve both when omitted",
			sarahPIN:  "",
			johnPIN:   "",
			wantSarah: "1234",
			wantJohn:  "5678",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			cfgPath := filepath.Join(tmp, "config.yaml")

			initialCfg := &config.Config{
				Users: []config.UserConfig{
					{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
					{Name: "john", DisplayName: "John", Role: "parent", PIN: "5678"},
				},
			}

			data, _ := yaml.Marshal(initialCfg)
			if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			s := &Server{
				cfg:     initialCfg,
				cfgPath: cfgPath,
				cfgMu:   sync.RWMutex{},
			}

			body := `{
				"users": [
					{"name": "sarah", "display_name": "Sarah", "role": "parent", "pin": "` + tc.sarahPIN + `"},
					{"name": "john", "display_name": "John", "role": "parent", "pin": "` + tc.johnPIN + `"}
				]
			}`

			req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
			rec := httptest.NewRecorder()
			s.handleSettingsPost(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("POST failed: %d (body: %s)", rec.Code, rec.Body.String())
			}

			s.cfgMu.RLock()
			defer s.cfgMu.RUnlock()

			for _, u := range s.cfg.Users {
				if u.Name == "sarah" && u.PIN != tc.wantSarah {
					t.Errorf("sarah PIN = %q, want %q", u.PIN, tc.wantSarah)
				}
				if u.Name == "john" && u.PIN != tc.wantJohn {
					t.Errorf("john PIN = %q, want %q", u.PIN, tc.wantJohn)
				}
			}
		})
	}
}

// TestSettingsPost_ReorderUnknownUser verifies that an unknown user in the
// POST body does not inherit a PIN from an existing user, and reordering
// does not cause cross-assignment.
func TestSettingsPost_ReorderUnknownUser(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
			{Name: "john", DisplayName: "John", Role: "parent", PIN: "5678"},
		},
	}

	data, _ := yaml.Marshal(initialCfg)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// POST with reordered users + a new unknown user (omits PIN — should NOT inherit)
	body := `{
		"users": [
			{"name": "john", "display_name": "John", "role": "parent"},
			{"name": "bob_new", "display_name": "Bob", "role": "parent"},
			{"name": "sarah", "display_name": "Sarah", "role": "parent"}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettingsPost(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d (body: %s)", rec.Code, rec.Body.String())
	}

	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	pinMap := make(map[string]string, len(s.cfg.Users))
	for _, u := range s.cfg.Users {
		pinMap[u.Name] = u.PIN
	}

	// Existing users keep their own PINs
	if pinMap["sarah"] != "1234" {
		t.Errorf("sarah PIN = %q, want 1234", pinMap["sarah"])
	}
	if pinMap["john"] != "5678" {
		t.Errorf("john PIN = %q, want 5678", pinMap["john"])
	}

	// Unknown user must NOT inherit someone else's PIN
	if pinMap["bob_new"] != "" {
		t.Errorf("unknown user bob_new inherited PIN %q — must not inherit another person's PIN", pinMap["bob_new"])
	}
}

// TestSettingsPost_PinPreservedCaseInsensitive verifies that PIN preservation
// matches the config's case-insensitive identity convention (Config.GetUser).
// An unknown user (truly new name) must still NOT inherit a PIN.
func TestSettingsPost_PinPreservedCaseInsensitive(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
			{Name: "john", DisplayName: "John", Role: "parent", PIN: "5678"},
		},
	}

	data, _ := yaml.Marshal(initialCfg)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// POST references the same two users with different casing (omitting PINs),
	// plus a genuinely new unknown user that must not inherit a PIN.
	body := `{
		"users": [
			{"name": "SARAH", "display_name": "Sarah", "role": "parent"},
			{"name": "John", "display_name": "John", "role": "parent"},
			{"name": "bob_new", "display_name": "Bob", "role": "parent"}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettingsPost(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed: %d (body: %s)", rec.Code, rec.Body.String())
	}

	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	// Case variants must have matched the existing users and preserved their
	// PINs via the shared case-insensitive identity convention.
	sarahPIN := s.cfg.GetUser("sarah")
	if sarahPIN == nil || sarahPIN.PIN != "1234" {
		t.Errorf("sarah PIN = %v, want 1234 (preserved despite case-variant name)", sarahPIN)
	}
	johnPIN := s.cfg.GetUser("john")
	if johnPIN == nil || johnPIN.PIN != "5678" {
		t.Errorf("john PIN = %v, want 5678 (preserved despite case-variant name)", johnPIN)
	}

	// The unknown user must not inherit anyone else's PIN.
	bobPIN := s.cfg.GetUser("bob_new")
	if bobPIN == nil || bobPIN.PIN != "" {
		t.Errorf("unknown user bob_new PIN = %v, want empty (no inheritance)", bobPIN)
	}
}

// TestSettingsPost_AllParentsRemoved verifies that removing all parents with
// PINs is rejected and does not mutate the live config or persisted file.
func TestSettingsPost_AllParentsRemoved(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
		},
	}

	data, _ := yaml.Marshal(initialCfg)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	originalContent, _ := os.ReadFile(cfgPath)

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// Convert the only parent to a child, leaving no parent with a PIN
	body := `{
		"users": [
			{"name": "sarah", "display_name": "Sarah", "role": "child"}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettingsPost(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least one parent") {
		t.Errorf("error body missing expected message: %s", rec.Body.String())
	}

	// Verify config was NOT mutated
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if len(s.cfg.Users) != 1 {
		t.Fatalf("users count = %d, want 1 (no mutation on rejection)", len(s.cfg.Users))
	}
	if s.cfg.Users[0].Role != "parent" {
		t.Errorf("sarah role = %q, want parent (no mutation on rejection)", s.cfg.Users[0].Role)
	}
	if s.cfg.Users[0].PIN != "1234" {
		t.Errorf("sarah PIN = %q, want 1234 (no mutation on rejection)", s.cfg.Users[0].PIN)
	}

	// Verify persisted file was NOT mutated
	currentContent, _ := os.ReadFile(cfgPath)
	if string(currentContent) != string(originalContent) {
		t.Errorf("persisted config file was mutated on rejected update")
	}
}

// TestSettingsWriteConfig_Atomic proves writeConfig persists the config
// atomically: a successful write lands a valid, parseable file with the
// managed header, and no leftover temp files remain.
func TestSettingsWriteConfig_Atomic(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "https://api.openai.com",
			Model:   "gpt-4",
		},
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
		},
	}

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	seed, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(cfgPath, seed, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := &Server{cfg: cfg, cfgPath: cfgPath, cfgMu: sync.RWMutex{}}
	if err := s.writeConfig(); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !bytes.HasPrefix(content, []byte("# FamClaw configuration")) {
		t.Errorf("written config missing managed header")
	}
	// No leftover .settings-* temp file after a clean write.
	leftovers, _ := filepath.Glob(filepath.Join(tmp, ".settings-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp file(s) left behind after successful write: %v", leftovers)
	}
}

// TestSettingsPost_WriteFailureRollsBackMemory exercises the real handler
// path: when persistence fails, handleSettingsPost must roll s.cfg back so
// memory and disk agree, return 500, and leave the on-disk config untouched.
func TestSettingsPost_WriteFailureRollsBackMemory(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "https://api.openai.com",
			Model:   "gpt-4",
		},
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
		},
		Gateways: config.GatewaysConfig{
			Telegram: config.TelegramConfig{Enabled: true, Token: "123456789:ABC"},
		},
		Tools: config.ToolsConfig{
			WebFetch: config.WebFetchConfig{
				Enabled:      true,
				URLAllowlist: []string{"example.com"},
			},
		},
	}

	seed, err := yaml.Marshal(initialCfg)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}

	// Make cfgPath a non-empty directory so the rename inside writeConfig
	// fails portably regardless of euid; the sentinel is the on-disk record.
	if err := os.MkdirAll(cfgPath, 0o700); err != nil {
		t.Fatalf("mkdir config target: %v", err)
	}
	sentinel := filepath.Join(cfgPath, "sentinel")
	if err := os.WriteFile(sentinel, seed, 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// Pre-POST in-memory snapshot of the sections the handler may replace.
	preLLM := initialCfg.LLM
	preUsers := make([]config.UserConfig, len(initialCfg.Users))
	copy(preUsers, initialCfg.Users)
	preGateways := initialCfg.Gateways
	preTools := initialCfg.Tools
	if initialCfg.Tools.WebFetch.URLAllowlist != nil {
		allow := make([]string, len(initialCfg.Tools.WebFetch.URLAllowlist))
		copy(allow, initialCfg.Tools.WebFetch.URLAllowlist)
		preTools.WebFetch.URLAllowlist = allow
	}

	// POST that changes every managed section so a missing rollback is visible.
	body := `{
		"llm": {"base_url": "https://api.anthropic.com", "model": "claude-3"},
		"users": [
			{"name": "sarah", "display_name": "Sarah Updated", "role": "parent", "pin": "4321"}
		],
		"gateways": {
			"telegram": {"enabled": false, "token": "999:NEW"},
			"discord": {"enabled": true, "token": "987654321:ZYX"}
		},
		"web_fetch": {"enabled": true, "url_allowlist": ["example.com", "test.com"]}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}

	disk, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !bytes.Equal(disk, seed) {
		t.Errorf("on-disk config changed by failed POST:\n got=%s\nwant=%s", disk, seed)
	}

	leftovers, _ := filepath.Glob(filepath.Join(tmp, ".settings-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp file(s) left behind after failed write: %v", leftovers)
	}

	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if !reflect.DeepEqual(s.cfg.LLM, preLLM) {
		t.Errorf("LLM not rolled back: got %+v, want %+v", s.cfg.LLM, preLLM)
	}
	if !reflect.DeepEqual(s.cfg.Users, preUsers) {
		t.Errorf("Users not rolled back: got %+v, want %+v", s.cfg.Users, preUsers)
	}
	if !reflect.DeepEqual(s.cfg.Gateways, preGateways) {
		t.Errorf("Gateways not rolled back: got %+v, want %+v", s.cfg.Gateways, preGateways)
	}
	if !reflect.DeepEqual(s.cfg.Tools, preTools) {
		t.Errorf("Tools not rolled back: got %+v, want %+v", s.cfg.Tools, preTools)
	}
}

// TestSettingsPost_InvalidWebFetchHost verifies that an invalid host in the
// web_fetch allowlist leaves BOTH s.cfg and the persisted file byte-identical.
func TestSettingsPost_InvalidWebFetchHost(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")

	initialCfg := &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "https://api.openai.com",
			Model:   "gpt-4",
		},
		Users: []config.UserConfig{
			{Name: "sarah", DisplayName: "Sarah", Role: "parent", PIN: "1234"},
		},
		Tools: config.ToolsConfig{
			WebFetch: config.WebFetchConfig{
				Enabled:      true,
				URLAllowlist: []string{"valid.com"},
			},
		},
	}

	data, _ := yaml.Marshal(initialCfg)
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	originalContent, _ := os.ReadFile(cfgPath)

	s := &Server{
		cfg:     initialCfg,
		cfgPath: cfgPath,
		cfgMu:   sync.RWMutex{},
	}

	// POST a valid LLM change alongside an invalid web_fetch host
	body := `{
		"llm": {
			"base_url": "https://api.newhost.com",
			"model": "new-model"
		},
		"users": [
			{"name": "sarah", "display_name": "Sarah", "role": "parent"}
		],
		"web_fetch": {
			"enabled": true,
			"url_allowlist": ["valid..bad-host"]
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.handleSettingsPost(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}

	// Verify s.cfg was NOT mutated at all (not even the LLM part)
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg.LLM.BaseURL != "https://api.openai.com" {
		t.Errorf("LLM.BaseURL = %q, want original — no partial mutation allowed", s.cfg.LLM.BaseURL)
	}
	if s.cfg.Tools.WebFetch.Enabled != true {
		t.Errorf("WebFetch.Enabled changed on rejected update")
	}
	if len(s.cfg.Tools.WebFetch.URLAllowlist) != 1 || s.cfg.Tools.WebFetch.URLAllowlist[0] != "valid.com" {
		t.Errorf("WebFetch.URLAllowlist changed on rejected update: %v", s.cfg.Tools.WebFetch.URLAllowlist)
	}

	// Verify persisted file was NOT mutated
	currentContent, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(currentContent, originalContent) {
		t.Errorf("persisted config file differs after rejected update")
	}
}
