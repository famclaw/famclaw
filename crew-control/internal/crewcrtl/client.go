// Package crewcrtl implements a read-only MCP server wrapping firstmate's
// fleet scripts (fm-fleet-view.sh, fm-crew-state.sh) and the data/backlog.md
// file. It is part of the crew-control-mcp out-of-repo addon.
//
// Every tool here is strictly read-only: no crew is started, stopped,
// steered, or torn down. A crew ID supplied by a chat message is treated as
// hostile input — it is validated against a strict allowlist regex and passed
// to exec.CommandContext (which never invokes a shell), so shell
// metacharacters, path traversal, and command substitution cannot reach a
// shell.
package crewcrtl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultFirstMateHome is the firstmate home directory on the Linux box where
// the fleet state actually lives. Each CLI flag can override the individual
// paths derived from this.
const DefaultFirstMateHome = "/home/dep/tools/firstmate"

// scriptTimeout bounds how long any single fleet script may run before the
// context is cancelled and the process killed.
const scriptTimeout = 30 * time.Second

// crewIDRe is the strict allowlist for crew IDs. It permits only letters,
// digits, underscores, and dashes (starting with an alphanumeric). This
// rejects every shell metacharacter: ; ` $() $() ../ etc. Combined with
// exec.CommandContext (which never spawns a shell), a crew ID cannot reach a
// shell under any circumstance.
var crewIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateCrewID returns an error if id is empty or contains characters
// outside the safe allowlist. This is the security gate: a crew ID comes from
// a chat message (untrusted), so we reject anything that isn't a simple
// identifier before it ever touches a command line.
func ValidateCrewID(id string) error {
	if id == "" {
		return fmt.Errorf("crew_id is required")
	}
	if !crewIDRe.MatchString(id) {
		return fmt.Errorf("crew_id %q contains characters outside the allowed set "+
			"(letters, digits, dashes, underscores only) — rejected to prevent shell injection", id)
	}
	return nil
}

// Client wraps the firstmate fleet scripts and data for read-only querying.
type Client struct {
	scriptsDir string // directory containing fm-fleet-view.sh, fm-crew-state.sh
	dataDir    string // directory containing backlog.md
	stateDir   string // directory containing *.meta and *.status
	env        []string
}

// NewClient creates a Client configured for the given firstmate home.
// Individual paths can be overridden via the ClientConfig.
func NewClient(cfg ClientConfig) *Client {
	fmHome := cfg.FMHome
	if fmHome == "" {
		fmHome = DefaultFirstMateHome
	}

	scriptsDir := cfg.ScriptsDir
	if scriptsDir == "" {
		scriptsDir = filepath.Join(fmHome, "bin")
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(fmHome, "data")
	}
	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(fmHome, "state")
	}

	env := os.Environ()
	env = append(env, "FM_HOME="+fmHome)
	env = append(env, "FM_STATE_OVERRIDE="+stateDir)
	env = append(env, "FM_DATA_OVERRIDE="+dataDir)

	return &Client{
		scriptsDir: scriptsDir,
		dataDir:    dataDir,
		stateDir:   stateDir,
		env:        env,
	}
}

// ClientConfig configures a Client.
type ClientConfig struct {
	FMHome     string // firstmate home directory
	ScriptsDir string // path to bin/ (default: <fmHome>/bin)
	DataDir    string // path to data/ (default: <fmHome>/data)
	StateDir   string // path to state/ (default: <fmHome>/state)
}

// CrewOverview wraps bin/fm-fleet-view.sh, returning the human-readable
// Markdown fleet overview. If the underlying script fails, the error is
// wrapped and the partial stderr/stdout is returned as the first value.
func (c *Client) FleetOverview(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	script := filepath.Join(c.scriptsDir, "fm-fleet-view.sh")
	cmd := exec.CommandContext(ctx, script)
	cmd.Env = c.env
	cmd.Dir = c.scriptsDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return string(stdout), fmt.Errorf("running fm-fleet-view.sh: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	return string(stdout), nil
}

// CrewState wraps bin/fm-crew-state.sh <id>, returning the canonical
// one-line state: "state: <state> · source: <source> · <detail>".
// The crew ID is validated before the script is invoked.
func (c *Client) CrewState(ctx context.Context, id string) (string, error) {
	if err := ValidateCrewID(id); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	script := filepath.Join(c.scriptsDir, "fm-crew-state.sh")
	cmd := exec.CommandContext(ctx, script, id)
	cmd.Env = c.env
	cmd.Dir = c.scriptsDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return string(stdout), fmt.Errorf("running fm-crew-state.sh %q: %w: %s",
			id, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// Backlog reads data/backlog.md directly and returns the in-flight and queued
// items as a structured Markdown summary. It does NOT shell out to
// fm-fleet-snapshot.sh, whose jq-based assembly can hit the OS argument-list
// limit on large backlogs (the --argjson backlog "$BACKLOG_JSON" call exceeds
// ARG_MAX). Parsing in Go avoids that failure mode entirely.
func (c *Client) Backlog(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	path := filepath.Join(c.dataDir, "backlog.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading backlog %q: %w", path, err)
	}

	bl, err := ParseBacklog(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing backlog: %w", err)
	}
	return bl.FormatMarkdown(), nil
}
