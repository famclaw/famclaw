package wasmskill

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Default timeouts for skill session phases (zero values are replaced by
// these).
const (
	DefaultInitTimeout = 15 * time.Second
	// DefaultInvokeTimeout bounds one Invoke when the caller's context has
	// no deadline.
	DefaultInvokeTimeout = 15 * time.Second
)

// Config controls a one-shot Run or a Skill session. Zero fields take
// defaults.
type Config struct {
	// Name is the Wasm module instance name (default "skill").
	Name string

	// SandboxRoot, when non-empty, is the only filesystem view the guest
	// gets: exactly one directory, preopened and mounted at "/". When empty
	// (the default) the guest has no filesystem access at all (deny by
	// default).
	SandboxRoot string

	// Env are "KEY=VALUE" pairs exported to the guest as WASI environment.
	// Malformed entries are rejected at validation time.
	Env []string

	// InitTimeout bounds the wait for the guest's init response in
	// NewSkill (zero uses DefaultInitTimeout).
	InitTimeout time.Duration

	// InvokeTimeout bounds a single Invoke when the caller's context has no
	// deadline (zero uses DefaultInvokeTimeout).
	InvokeTimeout time.Duration
}

// effective returns a validated copy of c with zero fields set to defaults.
// A nil receiver is valid and yields defaults.
func (c *Config) effective() (Config, error) {
	if c == nil {
		c = &Config{}
	}
	cfg := c.fillDefaults()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// fillDefaults sets zero fields to defaults.
func (c Config) fillDefaults() Config {
	if c.Name == "" {
		c.Name = "skill"
	}
	if c.InitTimeout <= 0 {
		c.InitTimeout = DefaultInitTimeout
	}
	if c.InvokeTimeout <= 0 {
		c.InvokeTimeout = DefaultInvokeTimeout
	}
	return c
}

// validate reports configuration problems before any Wasm is loaded.
func (c Config) validate() error {
	if c.SandboxRoot != "" {
		fi, err := os.Stat(c.SandboxRoot)
		if err != nil {
			return fmt.Errorf("wasmskill: sandbox root: %w", err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("wasmskill: sandbox root %q is not a directory", c.SandboxRoot)
		}
	}
	for _, kv := range c.Env {
		if !validEnv(kv) {
			return fmt.Errorf("wasmskill: invalid env entry %q (want KEY=VALUE)", kv)
		}
	}
	return nil
}

// validEnv reports whether kv is a well-formed "KEY=VALUE" entry.
func validEnv(kv string) bool {
	return strings.IndexByte(kv, '=') > 0
}
