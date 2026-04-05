package agentcore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/honeybadger"
)

// SecurityScanDeps holds dependencies for the security scan stage.
type SecurityScanDeps struct {
	Client       *honeybadger.Client
	RescanDays   int  // re-scan if last scan older than N days (default 7)
	Enabled      bool // master switch
	// LastScanFunc returns the last scan time for a skill. nil = always scan.
	LastScanFunc func(skillName string) (time.Time, bool)
	// SaveScanFunc saves a scan result. Can be nil.
	SaveScanFunc func(skillName string, result *honeybadger.ScanResult)
}

// NewStageSecurityScan returns a stage that runs honeybadger on newly used
// or stale skills before they execute in the pipeline.
func NewStageSecurityScan(deps SecurityScanDeps) Stage {
	if deps.RescanDays == 0 {
		deps.RescanDays = 7
	}

	return func(ctx context.Context, turn *Turn) error {
		if !deps.Enabled || deps.Client == nil || !deps.Client.Available() {
			return nil // honeybadger not available, skip
		}

		// Check each tool's source skill
		for _, tool := range turn.Tools {
			if tool.Source != "mcp" || tool.ServerName == "" {
				continue
			}

			// Check if scan is needed
			if deps.LastScanFunc != nil {
				lastScan, found := deps.LastScanFunc(tool.ServerName)
				if found && time.Since(lastScan) < time.Duration(deps.RescanDays)*24*time.Hour {
					continue // scan is fresh
				}
			}

			log.Printf("[security] scanning skill %q before use", tool.ServerName)

			result, err := deps.Client.Scan(ctx, tool.ServerName, honeybadger.ScanOptions{})
			if err != nil {
				log.Printf("[security] scan error for %q: %v", tool.ServerName, err)
				continue // don't block on scan failures
			}

			if deps.SaveScanFunc != nil {
				deps.SaveScanFunc(tool.ServerName, result)
			}

			switch result.Verdict {
			case "FAIL":
				log.Printf("[security] FAIL for skill %q: score=%d", tool.ServerName, result.CVECount)
				turn.Output = fmt.Sprintf("A security scan found issues with the %s tool. A parent has been notified.", tool.ServerName)
				turn.SetMeta("security_blocked", tool.ServerName)
				// Remove the blocked tool
				var filtered []Tool
				for _, t := range turn.Tools {
					if t.ServerName != tool.ServerName {
						filtered = append(filtered, t)
					}
				}
				turn.Tools = filtered
			case "WARN":
				log.Printf("[security] WARN for skill %q: score=%d", tool.ServerName, result.CVECount)
				turn.SetMeta("security_warn", tool.ServerName)
			}
		}
		return nil
	}
}
