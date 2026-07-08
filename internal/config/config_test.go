package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLLMAPIKey(t *testing.T) {
	tests := []struct {
		name          string
		yamlKey       string
		envKey        string
		wantKey       string
		wantWarning   bool
	}{
		{
			name:          "env set yaml unset → env wins",
			yamlKey:       "",
			envKey:        "sk-env-only",
			wantKey:       "sk-env-only",
			wantWarning:   false,
		},
		{
			name:          "env set yaml set → env wins, no warning",
			yamlKey:       "sk-yaml-value",
			envKey:        "sk-env-wins",
			wantKey:       "sk-env-wins",
			wantWarning:   false,
		},
		{
			name:          "env unset yaml set → yaml used, warning logged",
			yamlKey:       "sk-yaml-value",
			envKey:        "",
			wantKey:       "sk-yaml-value",
			wantWarning:   true,
		},
		{
			name:          "env unset yaml unset → empty, no warning",
			yamlKey:       "",
			envKey:        "",
			wantKey:       "",
			wantWarning:   false,
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
