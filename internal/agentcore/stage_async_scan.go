package agentcore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/skillbridge"
)

// AsyncScanDeps holds dependencies for the async security scan stage.
type AsyncScanDeps struct {
	Scanner        skillbridge.Scanner
	Quarantine     *skillbridge.Quarantine
	LastScanFunc   func(scanTarget string) (time.Time, bool)
	SaveScanFunc   func(scanTarget string, result *honeybadger.ScanResult)
	RescanInterval time.Duration
	ScanTimeout    time.Duration
	Paranoia       string
	Enabled        bool
	BlockOnFail    bool
	NotifyOnBlock  bool
	NotifyFunc     func(title, body string) // simplified notify callback
}

// NewStageAsyncScan returns a stage that fires background goroutines to
// scan tools used in this turn. Never blocks the turn. Results land in
// the quarantine for the NEXT turn's filter stage to act on.
func NewStageAsyncScan(deps AsyncScanDeps) Stage {
	if deps.ScanTimeout == 0 {
		deps.ScanTimeout = 60 * time.Second
	}
	if deps.RescanInterval == 0 {
		deps.RescanInterval = 7 * 24 * time.Hour
	}

	return func(_ context.Context, turn *Turn) error {
		if !deps.Enabled || deps.Scanner == nil || !deps.Scanner.Available() {
			return nil
		}

		for _, tool := range turn.Tools {
			if tool.ScanTarget == "" {
				continue
			}

			// Skip if recently scanned
			if deps.LastScanFunc != nil {
				lastScan, found := deps.LastScanFunc(tool.ScanTarget)
				if found && time.Since(lastScan) < deps.RescanInterval {
					continue
				}
			}

			// Fire and forget — use background context so this survives
			// the turn's context being canceled when the response returns.
			t := tool
			go deps.scanAsync(t)
		}
		return nil
	}
}

func (d *AsyncScanDeps) scanAsync(tool Tool) {
	ctx, cancel := context.WithTimeout(context.Background(), d.ScanTimeout)
	defer cancel()

	result, err := d.Scanner.Scan(ctx, tool.ScanTarget, honeybadger.ScanOptions{
		Paranoia: d.Paranoia,
	})
	if err != nil {
		log.Printf("[security] async scan error for %q: %v", tool.ScanTarget, err)
		return
	}

	if d.SaveScanFunc != nil {
		d.SaveScanFunc(tool.ScanTarget, result)
	}

	if result.Verdict == "FAIL" && d.BlockOnFail && d.Quarantine != nil {
		if err := d.Quarantine.Block(ctx, tool.Name, tool.ScanTarget, result); err != nil {
			log.Printf("[security] failed to quarantine %q: %v", tool.ScanTarget, err)
		} else {
			log.Printf("[security] QUARANTINED %q: %s", tool.ScanTarget, result.Reasoning)
		}

		if d.NotifyOnBlock && d.NotifyFunc != nil {
			d.NotifyFunc(
				"Security: tool quarantined",
				fmt.Sprintf("%s was quarantined after security scan: %s", tool.Name, result.Reasoning),
			)
		}
	}
}
