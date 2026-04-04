package honeybadger

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// Scan runs honeybadger on a repo URL and returns the result.
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

	args := []string{"scan", repoURL, "--format", "ndjson"}
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

	// Read ndjson stream — last line is the summary result
	var result ScanResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var line json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		// Try to parse as ScanResult (summary line)
		var candidate ScanResult
		if json.Unmarshal(scanner.Bytes(), &candidate) == nil && candidate.Verdict != "" {
			result = candidate
		}
		// Try to parse as Finding (detail line)
		var finding Finding
		if json.Unmarshal(scanner.Bytes(), &finding) == nil && finding.Severity != "" {
			result.Findings = append(result.Findings, finding)
		}
	}

	if err := cmd.Wait(); err != nil {
		// honeybadger exits 1 on FAIL verdict — that's expected
		if result.Verdict == "FAIL" {
			return &result, nil
		}
		return nil, fmt.Errorf("honeybadger exited with error: %w", err)
	}

	if result.ScannedAt.IsZero() {
		result.ScannedAt = time.Now()
	}

	return &result, nil
}
