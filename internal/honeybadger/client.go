package honeybadger

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
)

// Client spawns the honeybadger binary and reads its ndjson output.
type Client struct{}

// New creates a HoneyBadger client.
func New() *Client {
	return &Client{}
}

// Available returns true if the honeybadger binary is in PATH.
func (c *Client) Available() bool {
	_, err := exec.LookPath("honeybadger")
	return err == nil
}

// EnsureScanner makes famclaw self-sufficient: if the honeybadger binary is
// already in PATH it is a no-op. Otherwise it attempts to fetch and install it
// via `go install` (the famclaw install/update path must not leave a fresh
// machine with a scanner that "looks armed but cannot scan"). If the go
// toolchain is missing or the install fails, it returns a clear error so the
// caller can fail closed instead of silently pretending to scan.
func (c *Client) EnsureScanner(ctx context.Context, version string) error {
	if c.Available() {
		return nil
	}
	if version == "" {
		version = HoneyBadgerVersion
	}
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("honeybadger binary not in PATH and go toolchain unavailable to fetch it "+
			"(install manually: go install github.com/famclaw/honeybadger/cmd/honeybadger@%s)", version)
	}
	log.Printf("[honeybadger] fetching scanner %s via go install...", version)
	cmd := exec.CommandContext(ctx, "go", "install", fmt.Sprintf("github.com/famclaw/honeybadger/cmd/honeybadger@%s", version))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetching honeybadger %s: %w", version, err)
	}
	if !c.Available() {
		return fmt.Errorf("honeybadger %s installed but not found in PATH after go install", version)
	}
	log.Printf("[honeybadger] scanner installed ✅")
	return nil
}

// Scan runs honeybadger on a repo URL / local path and returns the result.
// If the binary is not available, returns an error.
func (c *Client) Scan(ctx context.Context, repoURL string, opts ScanOptions) (*ScanResult, error) {
	if opts.Force {
		return &ScanResult{
			Verdict:   "SKIP",
			Reasoning: "Scan skipped (--force)",
			ScannedAt: time.Now(),
		}, nil
	}

	if !c.Available() {
		return nil, fmt.Errorf("honeybadger binary not found in PATH — install with: go install github.com/famclaw/honeybadger/cmd/honeybadger@latest")
	}

	args := []string{"scan", repoURL, "--format", "ndjson", "--offline"}
	if !opts.Offline {
		// drop the offline flag we pre-pended; online mode re-enables network checks
		args = []string{"scan", repoURL, "--format", "ndjson"}
	}
	if opts.Paranoia != "" {
		args = append(args, "--paranoia", opts.Paranoia)
	}
	if opts.InstalledSHA != "" {
		args = append(args, "--installed-sha", opts.InstalledSHA)
	}
	if opts.InstalledToolHash != "" {
		args = append(args, "--tool-hash", opts.InstalledToolHash)
	}
	if opts.Path != "" {
		args = append(args, "--path", opts.Path)
	}

	cmd := exec.CommandContext(ctx, "honeybadger", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting honeybadger: %w", err)
	}

	// Read ndjson stream — dispatch on the "type" field.
	var result ScanResult
	foundResult := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue // not JSON — ignore
		}
		switch env.Type {
		case "result":
			var r ScanResult
			if json.Unmarshal(scanner.Bytes(), &r) == nil && r.Verdict != "" {
				result = r
				foundResult = true
			}
		case "finding":
			var f Finding
			if json.Unmarshal(scanner.Bytes(), &f) == nil && f.Severity != "" {
				result.Findings = append(result.Findings, f)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		// honeybadger exits non-zero on FAIL verdict — that's expected.
		if foundResult && result.Verdict == "FAIL" {
			return &result, nil
		}
		return nil, fmt.Errorf("honeybadger exited with error: %w", err)
	}

	if !foundResult {
		return nil, fmt.Errorf("honeybadger produced no result line for %q", repoURL)
	}

	if result.ScannedAt.IsZero() {
		result.ScannedAt = time.Now()
	}
	return &result, nil
}
