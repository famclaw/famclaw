// Package honeybadger provides a thin client for the HoneyBadger security scanner.
// All scanning logic lives in github.com/famclaw/honeybadger — this is just the
// client that spawns the binary and reads its ndjson output.
package honeybadger

import "time"

// HoneyBadgerVersion is the default scanner version pin used by EnsureScanner
// when no explicit version is configured. Pinned for reproducibility; bump in
// lockstep with the honeybadger release whose rules this famclaw build ships
// against.
const HoneyBadgerVersion = "v0.6.2"

// ScanOptions configures a HoneyBadger scan.
type ScanOptions struct {
	Paranoia          string // off|minimal|family|strict|paranoid
	InstalledSHA      string // for update verification
	InstalledToolHash string // for rug-pull detection
	Attested          bool   // was previous version attested
	Path              string // subdirectory for monorepos
	Offline           bool   // scan local path without network checks (faster, deterministic)
	Force             bool   // skip scan entirely
}

// ScanResult is the outcome of a HoneyBadger scan.
type ScanResult struct {
	Verdict    string    `json:"verdict"` // PASS | WARN | FAIL
	Reasoning  string    `json:"reasoning"`
	KeyFinding string    `json:"key_finding"`
	Findings   []Finding `json:"findings"`
	CVECount   int       `json:"cve_count"`
	Attested   bool      `json:"attested"`
	Tier       string    `json:"tier"` // api | chrome | offline
	ScannedAt  time.Time `json:"scanned_at"`
}

// Finding is a single issue found by the scanner.
type Finding struct {
	Severity    string `json:"severity"` // critical | high | medium | low | info
	Title       string `json:"title"`
	Description string `json:"description"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Scanner     string `json:"scanner"`
}

// Score maps the scan verdict to a 0-100 integer for the seccheck_reports
// table. PASS/SKIP = 100, FAIL = 0, WARN degrades by finding severity.
// Shared by every call site that persists a report so the scoring is
// consistent between skill scans and runtime tool scans.
func (r *ScanResult) Score() int {
	switch r.Verdict {
	case "PASS", "SKIP":
		return 100
	case "FAIL":
		return 0
	case "WARN":
		score := 100
		for _, f := range r.Findings {
			switch f.Severity {
			case "critical":
				score -= 40
			case "high":
				score -= 20
			case "medium":
				score -= 10
			case "low":
				score -= 5
			case "info":
				score -= 1
			}
		}
		if score < 1 {
			return 1
		}
		return score
	default:
		return 0
	}
}
