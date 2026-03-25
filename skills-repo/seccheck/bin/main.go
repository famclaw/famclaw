// Command seccheck is a CLI wrapper around the FamClaw security scanner.
// It scans a skill or MCP repository for security issues and prints a report.
//
// Usage: seccheck <repo-url-or-path>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/famclaw/famclaw/internal/seccheck"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: seccheck <repo-url-or-path>\n")
		os.Exit(2)
	}

	target := os.Args[1]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	scanner := seccheck.New(seccheck.Options{Verbose: true})
	report, err := scanner.Scan(ctx, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seccheck: %v\n", err)
		os.Exit(1)
	}

	// Print human-readable summary to stderr
	fmt.Fprintf(os.Stderr, "\nSecCheck Report: %s\n", target)
	fmt.Fprintf(os.Stderr, "Score: %d/100 — %s\n\n", report.Score, report.Verdict)

	for _, f := range report.Findings {
		fmt.Fprintf(os.Stderr, "  [%s] %s\n", f.Severity, f.Description)
	}

	if report.Summary != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", report.Summary)
	}

	// Print JSON to stdout (for MCP tool integration)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(report)

	if report.Verdict == "FAIL" {
		os.Exit(1)
	}
}
