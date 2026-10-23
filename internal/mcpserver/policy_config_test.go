package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "android-control.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing policy fixture: %v", err)
	}
	return p
}

func TestLoadPolicyConfig(t *testing.T) {
	const validFull = `
device_alias: famclaw-phone
screen_policy: wake_on_request
rate_limit: {max_actions_per_turn: 10, min_interval_ms: 50}
tools: {shell: true}
apps:
  - package: org.example.pilotapp
    mode: full
    actions_allow: [screenshot, find, tap, type, swipe, keyevent, open_app]
  - package: com.android.settings
    mode: read
deny: [com.android.chrome]
`

	tests := []struct {
		name     string
		content  string
		wantErr  bool
		validate func(t *testing.T, cfg *PolicyConfig)
	}{
		{
			name:    "empty path returns safe defaults",
			content: "", // special-cased: empty path, not empty file
			validate: func(t *testing.T, cfg *PolicyConfig) {
				if cfg.Tools.Shell {
					t.Errorf("default shell should be off")
				}
				if cfg.ScreenPolicy != "refuse_when_off" {
					t.Errorf("default screen_policy = %q, want refuse_when_off", cfg.ScreenPolicy)
				}
				if cfg.RateLimit.MaxActionsPerTurn != 30 || cfg.RateLimit.MinIntervalMs != 150 {
					t.Errorf("default rate limit = %+v, want {30 150}", cfg.RateLimit)
				}
				if len(cfg.Apps) != 0 || len(cfg.Deny) != 0 {
					t.Errorf("default allowlist should be empty, got apps=%v deny=%v", cfg.Apps, cfg.Deny)
				}
			},
		},
		{
			name:    "valid full config parses",
			content: validFull,
			validate: func(t *testing.T, cfg *PolicyConfig) {
				if cfg.DeviceAlias != "famclaw-phone" {
					t.Errorf("device alias = %q", cfg.DeviceAlias)
				}
				if cfg.ScreenPolicy != "wake_on_request" {
					t.Errorf("screen policy = %q", cfg.ScreenPolicy)
				}
				if !cfg.Tools.Shell {
					t.Errorf("shell should be enabled")
				}
				if len(cfg.Apps) != 2 {
					t.Fatalf("apps = %d, want 2", len(cfg.Apps))
				}
				if cfg.Apps[0].Mode != "full" || len(cfg.Apps[0].ActionsAllow) != 7 {
					t.Errorf("app[0] = %+v", cfg.Apps[0])
				}
				if cfg.Apps[1].NormalisedMode() != ModeReadOnly {
					t.Errorf("app[1] NormalisedMode = %q, want read_only", cfg.Apps[1].NormalisedMode())
				}
				if len(cfg.Deny) != 1 || cfg.Deny[0] != "com.android.chrome" {
					t.Errorf("deny = %v", cfg.Deny)
				}
			},
		},
		{
			name:    "unknown screen policy rejected",
			content: "screen_policy: maybe\n",
			wantErr: true,
		},
		{
			name:    "unknown app mode rejected",
			content: "apps:\n  - package: com.x\n    mode: admin\n",
			wantErr: true,
		},
		{
			name:    "zero max actions per turn rejected",
			content: "rate_limit: {max_actions_per_turn: 0}\n",
			wantErr: true,
		},
		{
			name:    "negative min interval rejected",
			content: "rate_limit: {min_interval_ms: -1}\n",
			wantErr: true,
		},
		{
			name:    "duplicate app package rejected",
			content: "apps:\n  - package: com.x\n    mode: full\n  - package: com.x\n    mode: read_only\n",
			wantErr: true,
		},
		{
			name:    "package in apps and deny rejected",
			content: "apps:\n  - package: com.x\n    mode: full\ndeny: [com.x]\n",
			wantErr: true,
		},
		{
			name:    "empty app package rejected",
			content: "apps:\n  - mode: full\n",
			wantErr: true,
		},
		{
			name:    "malformed yaml rejected",
			content: "device_alias: [unclosed\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.content != "" {
				path = writePolicy(t, tt.content)
			}
			cfg, err := LoadPolicyConfig(path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
	}
}

func TestLoadPolicyConfig_MissingFile(t *testing.T) {
	_, err := LoadPolicyConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestAppEntryNormalisedMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"full", ModeFull},
		{"read", ModeReadOnly},
		{"read_only", ModeReadOnly},
		{"READ", ModeReadOnly},
		{"deny", ModeDeny},
	}
	for _, tt := range tests {
		got := AppEntry{Mode: tt.in}.NormalisedMode()
		if got != tt.want {
			t.Errorf("NormalisedMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
