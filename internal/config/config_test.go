package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveLLMAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		yamlKey     string
		envKey      string
		wantKey     string
		wantWarning bool
	}{
		{
			name:        "env set yaml unset → env wins",
			yamlKey:     "",
			envKey:      "sk-env-only",
			wantKey:     "sk-env-only",
			wantWarning: false,
		},
		{
			name:        "env set yaml set → env wins, no warning",
			yamlKey:     "sk-yaml-value",
			envKey:      "sk-env-wins",
			wantKey:     "sk-env-wins",
			wantWarning: false,
		},
		{
			name:        "env unset yaml set → yaml used, warning logged",
			yamlKey:     "sk-yaml-value",
			envKey:      "",
			wantKey:     "sk-yaml-value",
			wantWarning: true,
		},
		{
			name:        "env unset yaml unset → empty, no warning",
			yamlKey:     "",
			envKey:      "",
			wantKey:     "",
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture log output
			var buf bytes.Buffer
			log.SetOutput(&buf)
			defer log.SetOutput(os.Stderr)

			// Save and restore original env var
			origEnv, envSet := os.LookupEnv("FAMCLAW_LLM_API_KEY")
			if envSet {
				defer os.Setenv("FAMCLAW_LLM_API_KEY", origEnv)
			} else {
				defer os.Unsetenv("FAMCLAW_LLM_API_KEY")
			}

			if tt.envKey != "" {
				os.Setenv("FAMCLAW_LLM_API_KEY", tt.envKey)
			} else {
				os.Unsetenv("FAMCLAW_LLM_API_KEY")
			}

			c := &Config{LLM: LLMConfig{APIKey: tt.yamlKey}}
			resolveLLMAPIKey(c)

			if c.LLM.APIKey != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", c.LLM.APIKey, tt.wantKey)
			}

			logOutput := buf.String()
			hasWarning := strings.Contains(logOutput, "plaintext YAML")
			if hasWarning != tt.wantWarning {
				t.Errorf("warning present = %v, want %v (log output: %q)", hasWarning, tt.wantWarning, logOutput)
			}

			// Ensure no key value appears in log output (skip empty)
			if tt.yamlKey != "" && strings.Contains(logOutput, tt.yamlKey) {
				t.Errorf("log output contains the YAML key value: %q", logOutput)
			}
			if tt.envKey != "" && strings.Contains(logOutput, tt.envKey) {
				t.Errorf("log output contains the env key value: %q", logOutput)
			}
		})
	}
}

func TestLoadEnvOverYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `llm:
  base_url: "http://localhost:11434"
  model: "llama3"
  api_key: "sk-from-yaml"`

	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Save and restore original env var
	origEnv, envSet := os.LookupEnv("FAMCLAW_LLM_API_KEY")
	if envSet {
		defer os.Setenv("FAMCLAW_LLM_API_KEY", origEnv)
	} else {
		defer os.Unsetenv("FAMCLAW_LLM_API_KEY")
	}

	os.Setenv("FAMCLAW_LLM_API_KEY", "sk-from-env")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.LLM.APIKey != "sk-from-env" {
		t.Errorf("APIKey = %q, want %q (env should override YAML)", cfg.LLM.APIKey, "sk-from-env")
	}
}

func TestLoadYAMLKeyWhenNoEnv(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `llm:
  base_url: "http://localhost:11434"
  model: "llama3"
  api_key: "sk-from-yaml"`

	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Save and restore original env var
	origEnv, envSet := os.LookupEnv("FAMCLAW_LLM_API_KEY")
	if envSet {
		defer os.Setenv("FAMCLAW_LLM_API_KEY", origEnv)
	} else {
		defer os.Unsetenv("FAMCLAW_LLM_API_KEY")
	}

	os.Unsetenv("FAMCLAW_LLM_API_KEY")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.LLM.APIKey != "sk-from-yaml" {
		t.Errorf("APIKey = %q, want %q (YAML should be used when no env)", cfg.LLM.APIKey, "sk-from-yaml")
	}
}

func TestApplyDefaults_SandboxRootDefault(t *testing.T) {
	tests := []struct {
		name            string
		sandboxRoot     string
		expectedDefault string
	}{
		{
			name:            "empty sandbox_root gets default",
			sandboxRoot:     "",
			expectedDefault: "./data/sandbox",
		},
		{
			name:            "non-empty sandbox_root preserved",
			sandboxRoot:     "/custom/sandbox",
			expectedDefault: "/custom/sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Tools: ToolsConfig{
					SandboxRoot: tt.sandboxRoot,
					Sandbox:     SandboxConfig{},
				},
				Server: ServerConfig{
					Host: "0.0.0.0",
					Port: 8080,
				},
				LLM: LLMConfig{
					MaxContextTokens:  4096,
					MaxResponseTokens: 512,
					Temperature:       0.7,
				},
				Approval: ApprovalConfig{
					ExpiryHours: 24,
				},
				Skills: SkillsConfig{
					Dir: "./skills",
				},
				Storage: StorageConfig{
					DBPath: "./data/famclaw.db",
				},
				Notifications: NotificationsConfig{
					Ntfy: NtfyConfig{
						URL: "http://localhost:2586",
					},
				},
			}
			applyDefaults(c)
			if c.Tools.SandboxRoot != tt.expectedDefault {
				t.Errorf("SandboxRoot = %q, want %q", c.Tools.SandboxRoot, tt.expectedDefault)
			}
		})
	}
}

func TestValidate_SandboxRootDefaultCreatesDir(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	defer os.Chdir(originalDir)
	os.Chdir(tmpDir)

	// Create config with empty sandbox root
	c := &Config{
		Tools: ToolsConfig{
			SandboxRoot: "",
		},
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		LLM: LLMConfig{
			MaxContextTokens:  4096,
			MaxResponseTokens: 512,
			Temperature:       0.7,
		},
		Approval: ApprovalConfig{
			ExpiryHours: 24,
		},
		Skills: SkillsConfig{
			Dir: "./skills",
		},
		Storage: StorageConfig{
			DBPath: "./data/famclaw.db",
		},
		Notifications: NotificationsConfig{
			Ntfy: NtfyConfig{
				URL: "http://localhost:2586",
			},
		},
	}

	// Apply defaults and validate
	applyDefaults(c)
	err := c.Validate()

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Check if the default sandbox directory was created
	expectedDir := "./data/sandbox"
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("expected sandbox directory %q was not created", expectedDir)
	}
}

func TestSandboxEnabled_IsEnabled(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantEnabled bool
	}{
		{
			name:        "unset sandbox block",
			yaml:        ``,
			wantEnabled: true,
		},
		{
			name: "enabled: false",
			yaml: `tools:
  sandbox:
    enabled: false`,
			wantEnabled: false,
		},
		{
			name: "enabled: true",
			yaml: `tools:
  sandbox:
    enabled: true`,
			wantEnabled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0o600); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}
			applyDefaults(cfg)
			if got := cfg.Tools.Sandbox.IsEnabled(); got != tt.wantEnabled {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.wantEnabled)
			}
		})
	}
}

func TestValidate_SandboxRootErrors(t *testing.T) {
	// Common config fields
	serverCfg := ServerConfig{
		Host: "0.0.0.0",
		Port: 8080,
	}
	llmCfg := LLMConfig{
		MaxContextTokens:  4096,
		MaxResponseTokens: 512,
		Temperature:       0.7,
		TimeoutSeconds:    300,
	}
	approvalCfg := ApprovalConfig{
		ExpiryHours: 24,
	}
	skillsCfg := SkillsConfig{
		Dir: "./skills",
	}
	storageCfg := StorageConfig{
		DBPath: "./data/famclaw.db",
	}
	notificationsCfg := NotificationsConfig{
		Ntfy: NtfyConfig{
			URL: "http://localhost:2586",
		},
	}

	tests := []struct {
		name            string
		sandboxRootFunc func() string
		setupFunc       func(string) error
		wantErr         bool
		errContains     string
		checkPathInErr  bool
	}{
		{
			name: "sandbox_root is a file",
			sandboxRootFunc: func() string {
				tmp := t.TempDir()
				return filepath.Join(tmp, "file")
			},
			setupFunc: func(path string) error {
				return os.WriteFile(path, []byte(""), 0644)
			},
			wantErr:        true,
			errContains:    "not a directory", // note: wrapped error says "not a directory"
			checkPathInErr: true,
		},
		{
			name: "sandbox_root parent not writable",
			sandboxRootFunc: func() string {
				tmp := t.TempDir()
				parent := filepath.Join(tmp, "parent")
				child := filepath.Join(parent, "child")
				// Create parent directory but make it unwritable
				os.MkdirAll(parent, 0o700)
				os.Chmod(parent, 0o000) // prevent writing
				return child
			},
			setupFunc: func(path string) error {
				// No additional setup needed; parent permissions already set
				return nil
			},
			wantErr:        true,
			errContains:    "permission denied", // from mkdir
			checkPathInErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandboxRoot := tt.sandboxRootFunc()
			// Setup
			if tt.setupFunc != nil {
				if err := tt.setupFunc(sandboxRoot); err != nil {
					t.Fatalf("setup failed: %v", err)
				}
			}

			c := &Config{
				Tools: ToolsConfig{
					SandboxRoot: sandboxRoot,
				},
				Server:        serverCfg,
				LLM:           llmCfg,
				Approval:      approvalCfg,
				Skills:        skillsCfg,
				Storage:       storageCfg,
				Notifications: notificationsCfg,
			}

			err := c.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				if tt.checkPathInErr {
					if !strings.Contains(err.Error(), sandboxRoot) {
						t.Errorf("error %q does not contain sandboxRoot %q", err.Error(), sandboxRoot)
					}
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestLLMTimeoutDefaultAndEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		timeoutSeconds int
		wantTimeout    time.Duration
	}{
		{
			name:           "zero defaults to 300s",
			timeoutSeconds: 0,
			wantTimeout:    300 * time.Second,
		},
		{
			name:           "explicit 60s",
			timeoutSeconds: 60,
			wantTimeout:    60 * time.Second,
		},
		{
			name:           "explicit 120s",
			timeoutSeconds: 120,
			wantTimeout:    120 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				LLM: LLMConfig{
					BaseURL:        "http://localhost:11434",
					Model:          "llama3",
					TimeoutSeconds: tt.timeoutSeconds,
				},
			}
			applyDefaults(c)
			if c.LLM.TimeoutSeconds != int(tt.wantTimeout.Seconds()) {
				t.Errorf("TimeoutSeconds = %d, want %d", c.LLM.TimeoutSeconds, int(tt.wantTimeout.Seconds()))
			}
			ep := c.LLMEndpointFor(nil)
			if ep.Timeout != tt.wantTimeout {
				t.Errorf("endpoint Timeout = %v, want %v", ep.Timeout, tt.wantTimeout)
			}
		})
	}
}

func TestLLMTimeoutValidation(t *testing.T) {
	tests := []struct {
		name           string
		timeoutSeconds int
		wantErr        bool
		errContains    string
	}{
		{
			name:           "zero is valid after defaults",
			timeoutSeconds: 0,
			wantErr:        false,
		},
		{
			name:           "positive is valid",
			timeoutSeconds: 300,
			wantErr:        false,
		},
		{
			name:           "negative is invalid",
			timeoutSeconds: -5,
			wantErr:        true,
			errContains:    "must be > 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				LLM: LLMConfig{
					BaseURL:        "http://localhost:11434",
					Model:          "llama3",
					TimeoutSeconds: tt.timeoutSeconds,
				},
			}
			applyDefaults(c)
			err := c.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}
