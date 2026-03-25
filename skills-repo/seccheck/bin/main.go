// Command seccheck is a CLI wrapper around the FamClaw security scanner.
// Exit codes: 0=PASS/WARN, 1=FAIL, 2=usage error, 3=runtime error
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

	scanner := seccheck.New(seccheck.Options{
		Verbose: true,
		Sandbox: "auto",
		Timeout: 5 * time.Minute,
		OSVAPI:  "https://api.osv.dev/v1",
	})
	report, err := scanner.Scan(ctx, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seccheck: %v\n", err)
		os.Exit(3)
	}

	// Human-readable summary to stderr
	fmt.Fprintf(os.Stderr, "\nSecCheck Report: %s\n", target)
	fmt.Fprintf(os.Stderr, "Score: %d/100 — %s\n\n", report.Score, report.Verdict)

	for _, f := range report.Findings {
		fmt.Fprintf(os.Stderr, "  [%s] %s\n", f.Severity, f.Description)
	}

	if report.Summary != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", report.Summary)
	}

	// JSON to stdout (for MCP tool integration)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(report)

	if report.Verdict == seccheck.VerdictFail {
		os.Exit(1)
	}
}
