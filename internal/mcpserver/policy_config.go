// Package mcpserver implements the android-control MCP server: the tool
// registry, dispatch entry point, and policy-file loader that will enforce
// the device-side safety model (app allowlist, per-app modes, shell kill
// switch) before any adb command is issued.
//
// This card delivers the skeleton: tools are registered with final JSON
// schemas, dispatch is a not-implemented stub, and the policy loader is
// complete but not yet enforced. See design doc §4–§5.
package mcpserver

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// PolicyConfig mirrors ~/.famclaw/android-control.yaml (design §5.2).
// Loaded once at server startup; the server is a single-client stdio
// process so there is no hot-reload requirement in v1.
type PolicyConfig struct {
	// DeviceAlias is the stable name shown in logs and audit entries.
	// The real adb serial is resolved internally and never appears in
	// logs or MCP responses.
	DeviceAlias string `yaml:"device_alias"`

	// DeviceSerial optionally pins the adb serial for DeviceAlias
	// (design §5.4: "or pinned in config"). Empty = auto-detect: with
	// exactly one online device it is used; with several, calls fail
	// closed until a pin is set.
	DeviceSerial string `yaml:"device_serial"`

	// ScreenPolicy: "refuse_when_off" (v1 default) or "wake_on_request".
	ScreenPolicy string `yaml:"screen_policy"`

	// RateLimit bounds device-action throughput per agent turn.
	RateLimit RateLimit `yaml:"rate_limit"`

	// Tools carries per-tool kill-switches. A tool not present defaults to
	// enabled; shell is the exception (defaults off).
	Tools map[string]bool `yaml:"tools"`

	// Apps is the allowlist. Each entry specifies an Android package
	// and its access mode.
	Apps []AppEntry `yaml:"apps"`

	// Deny is an explicit blocklist. Named deny always wins over allow.
	Deny []string `yaml:"deny"`
}

// RateLimit carries the two rate-limit knobs.
type RateLimit struct {
	MaxActionsPerTurn int `yaml:"max_actions_per_turn"`
	MinIntervalMs     int `yaml:"min_interval_ms"`
}

// AppEntry is one row in the app allowlist.
type AppEntry struct {
	Package      string   `yaml:"package"`
	Mode         string   `yaml:"mode"` // "full", "read" / "read_only", or "deny"
	ActionsAllow []string `yaml:"actions_allow"`
}

// Valid mode constants.
const (
	ModeFull     = "full"
	ModeReadOnly = "read_only"
	ModeDeny     = "deny"
)

// LoadPolicyConfig reads the YAML file at path, validates it, and returns a
// fully-populated PolicyConfig with defaults applied.
//
// When path is empty the function returns a zero-value PolicyConfig with
// safe defaults (shell disabled, screen_policy=refuse_when_off, no apps).
// This allows the server to start in "stub mode" before a policy file is
// provisioned.
func LoadPolicyConfig(path string) (*PolicyConfig, error) {
	cfg := &PolicyConfig{
		ScreenPolicy: "refuse_when_off",
		RateLimit: RateLimit{
			MaxActionsPerTurn: 30,
			MinIntervalMs:     150,
		},
	}

	if path == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading policy file %q: %w", path, err)
	}

	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parsing policy file %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating policy file %q: %w", path, err)
	}

	return cfg, nil
}

// validate enforces invariants that make the config safe:
//   - screen_policy is one of the known values
//   - every app entry has a non-empty package and a valid mode
//   - rate-limit values are positive
//   - no package appears in both apps and deny
func (c *PolicyConfig) validate() error {
	if c.ScreenPolicy != "refuse_when_off" && c.ScreenPolicy != "wake_on_request" {
		return fmt.Errorf("unknown screen_policy %q (want refuse_when_off or wake_on_request)", c.ScreenPolicy)
	}

	if c.RateLimit.MaxActionsPerTurn <= 0 {
		return fmt.Errorf("rate_limit.max_actions_per_turn must be > 0, got %d", c.RateLimit.MaxActionsPerTurn)
	}
	if c.RateLimit.MinIntervalMs < 0 {
		return fmt.Errorf("rate_limit.min_interval_ms must be >= 0, got %d", c.RateLimit.MinIntervalMs)
	}

	seen := make(map[string]bool, len(c.Apps)+len(c.Deny))
	for _, app := range c.Apps {
		if app.Package == "" {
			return fmt.Errorf("apps entry has empty package")
		}
		if err := validateAppMode(app.Mode); err != nil {
			return fmt.Errorf("apps entry %q: %w", app.Package, err)
		}
		if seen[app.Package] {
			return fmt.Errorf("duplicate package %q in apps list", app.Package)
		}
		seen[app.Package] = true
	}

	for _, pkg := range c.Deny {
		if pkg == "" {
			return fmt.Errorf("deny entry is empty")
		}
		if seen[pkg] {
			return fmt.Errorf("package %q appears in both apps and deny", pkg)
		}
		seen[pkg] = true
	}

	return nil
}

// validateAppMode accepts full, read, read_only, and deny.
func validateAppMode(mode string) error {
	normalised := strings.TrimSpace(strings.ToLower(mode))
	switch normalised {
	case "full", "read", "read_only", "deny":
		return nil
	default:
		return fmt.Errorf("unknown mode %q (want full, read/read_only, or deny)", mode)
	}
}

// NormalisedMode returns the canonical mode string for an AppEntry,
// mapping "read" → "read_only".
func (a AppEntry) NormalisedMode() string {
	m := strings.TrimSpace(strings.ToLower(a.Mode))
	if m == "read" {
		return ModeReadOnly
	}
	return m
}

// ToolEnabled reports whether the named tool is enabled by the tools map.
// Shell defaults to off (explicit opt-in required); every other tool
// defaults to on when its key is absent.
func (c *PolicyConfig) ToolEnabled(name string) bool {
	if name == "shell" {
		return c.Tools != nil && c.Tools["shell"]
	}
	if c.Tools == nil {
		return true
	}
	enabled, ok := c.Tools[name]
	if !ok {
		return true
	}
	return enabled
}

// AppMode returns the effective mode for the given Android package:
// ModeDeny, ModeReadOnly, or ModeFull. Deny-list entries win over allowlist
// entries; unlisted packages default to read-only (design §5.2).
func (c *PolicyConfig) AppMode(pkg string) string {
	for _, d := range c.Deny {
		if d == pkg {
			return ModeDeny
		}
	}
	for _, app := range c.Apps {
		if app.Package == pkg {
			return app.NormalisedMode()
		}
	}
	return ModeReadOnly
}
