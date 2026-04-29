//go:build e2e

// Package e2e provides end-to-end tests that start a real FamClaw server,
// exercise the HTTP API, and verify the full flow works.
// Run with: go test -tags e2e ./e2e/... -v
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var serverURL string

func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func TestMain(m *testing.M) {
	root := projectRoot()

	// Build the binary
	binName := "famclaw-e2e-test"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(os.TempDir(), binName)
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/famclaw")
	buildCmd.Dir = root
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build famclaw: %v\n", err)
		os.Exit(1)
	}

	// Write test config
	cfgPath := filepath.Join(os.TempDir(), "famclaw-e2e-config.yaml")
	dbPath := filepath.Join(os.TempDir(), "famclaw-e2e.db")
	os.Remove(dbPath) // clean slate
	cfg := fmt.Sprintf(`server:
  port: 18080
  secret: "e2e-test-secret-minimum-32-characters!"
llm:
  base_url: ""
  model: ""
  system_prompt: "You are a test assistant."
  max_context_tokens: 4096
  max_response_tokens: 512
  temperature: 0.7
users: []
storage:
  db_path: %s
`, dbPath)
	os.WriteFile(cfgPath, []byte(cfg), 0644)

	// Start server
	cmd := exec.Command(binPath, "--config", cfgPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Dir = root
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}

	serverURL = "http://localhost:18080"

	// Wait for server
	ready := false
	for i := 0; i < 30; i++ {
		resp, err := http.Get(serverURL + "/api/settings")
		if err == nil {
			resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		fmt.Fprintf(os.Stderr, "Server not ready after 15s\n")
		cmd.Process.Kill()
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// Cleanup
	cmd.Process.Kill()
	os.Remove(binPath)
	os.Remove(cfgPath)
	os.Remove(dbPath)
	os.Exit(code)
}

func TestWizardRedirect(t *testing.T) {
	// Root should redirect to /setup when unconfigured
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(serverURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("GET / status = %d, want 307", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/setup" {
		t.Errorf("redirect location = %q, want /setup", loc)
	}
}

func TestSetupServesWizard(t *testing.T) {
	resp, err := http.Get(serverURL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /setup = %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "FamClaw") {
		t.Error("/setup response doesn't contain 'FamClaw'")
	}
}

func TestHardwareDetectAPI(t *testing.T) {
	resp, err := http.Get(serverURL + "/api/setup/detect")
	if err != nil {
		t.Fatalf("GET /api/setup/detect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var hw map[string]any
	json.NewDecoder(resp.Body).Decode(&hw)

	if _, ok := hw["os"]; !ok {
		t.Error("missing 'os' field in hardware detect response")
	}
	if _, ok := hw["arch"]; !ok {
		t.Error("missing 'arch' field")
	}
	if _, ok := hw["total_ram_mb"]; !ok {
		t.Error("missing 'total_ram_mb' field")
	}
}

func TestSettingsAPIFirstBoot(t *testing.T) {
	resp, err := http.Get(serverURL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var settings map[string]any
	json.NewDecoder(resp.Body).Decode(&settings)

	if _, ok := settings["llm"]; !ok {
		t.Error("missing 'llm' in settings")
	}
}

func TestWizardSetupFlow(t *testing.T) {
	// First boot: POST settings without PIN (allowed)
	payload := `{
		"llm": {"base_url": "http://localhost:11434", "model": "test"},
		"users": [
			{"name": "testparent", "display_name": "Test Parent", "role": "parent", "pin": "5678"}
		],
		"gateways": {
			"telegram": {"enabled": false},
			"discord": {"enabled": false},
			"whatsapp": {"enabled": false}
		}
	}`

	resp, err := http.Post(serverURL+"/api/settings", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /api/settings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("wizard POST status = %d: %s", resp.StatusCode, body)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "saved" {
		t.Errorf("status = %q, want 'saved'", result["status"])
	}
}

func TestPINEnforcement(t *testing.T) {
	// After setup, POST without PIN should be rejected
	payload := `{"llm": {"base_url": "http://evil.com", "model": "x"}}`

	resp, err := http.Post(serverURL+"/api/settings", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST without PIN: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without PIN: status = %d, want 403", resp.StatusCode)
	}

	// POST with correct PIN should succeed
	req, _ := http.NewRequest("POST", serverURL+"/api/settings",
		strings.NewReader(`{"llm": {"base_url": "http://localhost:11434", "model": "test"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Parent-PIN", "5678")

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST with PIN: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Errorf("POST with PIN: status = %d: %s", resp2.StatusCode, body)
	}
}

func TestPINEnforcementWrongPIN(t *testing.T) {
	req, _ := http.NewRequest("POST", serverURL+"/api/settings",
		strings.NewReader(`{"llm": {"base_url": "http://localhost:11434", "model": "test"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Parent-PIN", "0000")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST with wrong PIN: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong PIN: status = %d, want 403", resp.StatusCode)
	}
}

func TestSettingsAfterSetup(t *testing.T) {
	// GET settings should show the configured user
	req, _ := http.NewRequest("GET", serverURL+"/api/settings", nil)
	req.Header.Set("X-Parent-PIN", "5678")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer resp.Body.Close()

	var settings struct {
		Users []struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"users"`
		LLM struct {
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
		} `json:"llm"`
	}
	json.NewDecoder(resp.Body).Decode(&settings)

	if len(settings.Users) == 0 {
		t.Fatal("no users after setup")
	}
	if settings.Users[0].Name != "testparent" {
		t.Errorf("user name = %q, want 'testparent'", settings.Users[0].Name)
	}
	if settings.LLM.BaseURL != "http://localhost:11434" {
		t.Errorf("llm.base_url = %q", settings.LLM.BaseURL)
	}
}

func TestNoSetupRedirectAfterConfig(t *testing.T) {
	// After configuration, root should NOT redirect to /setup
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(serverURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	// Should serve the app directly (200), not redirect (307)
	if resp.StatusCode == http.StatusTemporaryRedirect {
		t.Error("root still redirects to /setup after configuration — NeedsSetup() not updated")
	}
}
