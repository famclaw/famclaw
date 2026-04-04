package honeybadger

import (
	"context"
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
	// Verify ScanOptions struct works correctly
	opts := ScanOptions{
		Paranoia:         "family",
		InstalledSHA:     "abc123",
		InstalledToolHash: "def456",
		Path:             "subdir",
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
