package honeybadger

import (
	"context"
	"strings"
	"testing"
)

func TestAvailable(t *testing.T) {
	c := New()
	// Just verify it doesn't panic — honeybadger may or may not be installed
	_ = c.Available()
}

func TestScanForce(t *testing.T) {
	c := New()
	result, err := c.Scan(context.Background(), "https://github.com/example/repo", ScanOptions{
		Force: true,
	})
	if err != nil {
		t.Fatalf("force scan should not error: %v", err)
	}
	if result.Verdict != "SKIP" {
		t.Errorf("verdict = %q, want SKIP", result.Verdict)
	}
}

func TestScanNotAvailable(t *testing.T) {
	c := New()
	if c.Available() {
		t.Skip("honeybadger is installed — cannot test unavailable path")
	}
	_, err := c.Scan(context.Background(), "https://github.com/example/repo", ScanOptions{})
	if err == nil {
		t.Error("expected error when honeybadger not available")
	}
}

func TestScanOptionsArgs(t *testing.T) {
	opts := ScanOptions{
		Paranoia:          "family",
		InstalledSHA:      "abc123",
		InstalledToolHash: "def456",
		Path:              "subdir",
	}
	if opts.Paranoia != "family" {
		t.Errorf("paranoia = %q", opts.Paranoia)
	}
}

func TestScanResultTypes(t *testing.T) {
	result := ScanResult{
		Verdict:   "PASS",
		Reasoning: "All checks passed",
		Findings: []Finding{
			{Severity: "info", Title: "Clean", Scanner: "static"},
		},
	}
	if len(result.Findings) != 1 {
		t.Errorf("expected 1 finding, got %d", len(result.Findings))
	}
}

// ── verifyScanner tests (Finding 2) ────────────────────────────────────

// TestVerifyScannerAvailable checks that a correctly-installed honeybadger
// binary passes verification.
func TestVerifyScannerAvailable(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("honeybadger not in PATH")
	}
	if err := c.verifyScanner(context.Background(), HoneyBadgerVersion); err != nil {
		t.Fatalf("verifyScanner with correct version should succeed: %v", err)
	}
}

// TestVerifyScannerNotAvailable checks that verifyScanner returns a clear
// error when the binary is absent from PATH.
func TestVerifyScannerNotAvailable(t *testing.T) {
	c := New()
	if c.Available() {
		t.Skip("honeybadger is installed — cannot test unavailable path")
	}
	t.Setenv("PATH", t.TempDir()) // empty PATH — no binaries findable
	err := c.verifyScanner(context.Background(), HoneyBadgerVersion)
	if err == nil {
		t.Fatal("expected error when honeybadger not in PATH, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestVerifyScannerWrongVersion checks that a version mismatch is detected
// and rejected — a stale binary must not look armed.
func TestVerifyScannerWrongVersion(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("honeybadger not in PATH")
	}
	err := c.verifyScanner(context.Background(), "v99.0.0")
	if err == nil {
		t.Fatal("expected version mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("error should mention 'version mismatch', got: %v", err)
	}
}

// TestVerifyScannerDefaultVersion checks that an empty version pin defaults
// to HoneyBadgerVersion and succeeds.
func TestVerifyScannerDefaultVersion(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("honeybadger not in PATH")
	}
	if err := c.verifyScanner(context.Background(), ""); err != nil {
		t.Fatalf("verifyScanner with empty version should default to %s and succeed: %v", HoneyBadgerVersion, err)
	}
}

// TestEnsureScannerAvailableIsNoOp verifies that EnsureScanner returns nil
// (after verification) when the honeybadger binary is already in PATH and
// reports the correct version.
func TestEnsureScannerAvailableIsNoOp(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("honeybadger not in PATH")
	}
	if err := c.EnsureScanner(context.Background(), HoneyBadgerVersion); err != nil {
		t.Fatalf("EnsureScanner should succeed when binary is already available: %v", err)
	}
}

// TestEnsureScannerWrongVersionVerified verifies that when the honeybadger
// binary is in PATH but reports a different version than requested,
// EnsureScanner returns an error rather than silently accepting the
// mismatched binary.
func TestEnsureScannerWrongVersionVerified(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("honeybadger not in PATH")
	}
	err := c.EnsureScanner(context.Background(), "v99.0.0")
	if err == nil {
		t.Fatal("expected error when binary version does not match requested version, got nil")
	}
	if !strings.Contains(err.Error(), "verification failed") && !strings.Contains(err.Error(), "version mismatch") {
		t.Errorf("error should mention verification/version mismatch, got: %v", err)
	}
}
