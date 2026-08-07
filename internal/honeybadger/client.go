package honeybadger

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Primary path: go install (works on a machine with proxy/network access).
	log.Printf("[honeybadger] fetching scanner %s via go install...", version)
	installCmd := exec.CommandContext(ctx, "go", "install",
		fmt.Sprintf("github.com/famclaw/honeybadger/cmd/honeybadger@%s", version))
	installCmd.Stdout = os.Stderr
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		// Fallback: build from the module cache (works on networks where the
		// Go proxy is intercepted, as long as the module was previously
		// fetched/cached). Mirrors `make install-scanner` without the download.
		if cacheErr := c.buildFromCache(ctx, version); cacheErr != nil {
			return fmt.Errorf("fetching honeybadger %s: go install failed (%v); cache build also failed: %w",
				version, err, cacheErr)
		}
		log.Printf("[honeybadger] scanner installed from module cache ✅")
	}
	if !c.Available() {
		return fmt.Errorf("honeybadger %s installed but not found in PATH after fetch", version)
	}
	log.Printf("[honeybadger] scanner installed ✅")
	return nil
}

// buildFromCache compiles the honeybadger binary from the module cache
// (GOMODCACHE/github.com/famclaw/honeybadger@<version>/cmd/honeybadger) into
// GOBIN. Used when `go install pkg@version` fails (e.g. intercepted proxy) but
// the module is already cached.
func (c *Client) buildFromCache(ctx context.Context, version string) error {
	gomodcache, err := exec.CommandContext(ctx, "go", "env", "GOMODCACHE").Output()
	if err != nil {
		return fmt.Errorf("reading GOMODCACHE: %w", err)
	}
	gobin, err := exec.CommandContext(ctx, "go", "env", "GOBIN").Output()
	if err != nil {
		return fmt.Errorf("reading GOBIN: %w", err)
	}
	cacheDir := filepath.Join(strings.TrimSpace(string(gomodcache)),
		"github.com/famclaw/honeybadger@"+version)
	pkg := filepath.Join(cacheDir, "cmd", "honeybadger")
	if _, err := os.Stat(pkg); err != nil {
		return fmt.Errorf("honeybadger %s not in module cache at %s: %w", version, pkg, err)
	}
	out := strings.TrimSpace(string(gobin))
	if out == "" {
		gopath, _ := exec.CommandContext(ctx, "go", "env", "GOPATH").Output()
		out = filepath.Join(strings.TrimSpace(string(gopath)), "bin")
	}
	out = filepath.Join(out, "honeybadger")
	// Build from inside the cached module so Go uses honeybadger's own
	// go.mod (it is not a dependency of the famclaw module).
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", out, "./cmd/honeybadger")
	buildCmd.Dir = cacheDir
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("building honeybadger from cache: %w", err)
	}
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
				// The result line does not embed individual findings (those
				// arrive as separate "finding" lines), so merge rather than
				// overwrite the findings accumulated so far.
				r.Findings = append(r.Findings, result.Findings...)
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
		// honeybadger exits non-zero when findings exist (WARN=1, FAIL=2) and
		// only exits 0 on a clean PASS. The "result" line is authoritative,
		// so return it whenever we captured one rather than treating the exit
		// status as an error.
		if foundResult {
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
