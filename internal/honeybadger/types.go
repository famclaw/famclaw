// Package honeybadger provides a thin client for the HoneyBadger security scanner.
// All scanning logic lives in github.com/famclaw/honeybadger — this is just the
// client that spawns the binary and reads its ndjson output.
package honeybadger

import "time"

// ScanOptions configures a HoneyBadger scan.
type ScanOptions struct {
	Paranoia         string // minimal | family | strict | paranoid
	InstalledSHA     string // for update verification
	InstalledToolHash string // for rug-pull detection
	Attested         bool   // was previous version attested
	Path             string // subdirectory for monorepos
	Force            bool   // skip scan entirely
}

// ScanResult is the outcome of a HoneyBadger scan.
type ScanResult struct {
	Verdict    string    `json:"verdict"`     // PASS | WARN | FAIL
	Reasoning  string    `json:"reasoning"`
	KeyFinding string    `json:"key_finding"`
	Findings   []Finding `json:"findings"`
	CVECount   int       `json:"cve_count"`
	Attested   bool      `json:"attested"`
	Tier       string    `json:"tier"`        // api | chrome | offline
	ScannedAt  time.Time `json:"scanned_at"`
}

// Finding is a single issue found by the scanner.
type Finding struct {
	Severity    string `json:"severity"`    // critical | high | medium | low | info
	Title       string `json:"title"`
	Description string `json:"description"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Scanner     string `json:"scanner"`
}
