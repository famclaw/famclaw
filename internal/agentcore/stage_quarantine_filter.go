package agentcore

import (
	"context"
	"log"
)

// QuarantineChecker is the minimal interface for checking if a tool is quarantined.
type QuarantineChecker interface {
	IsBlocked(scanTarget string) bool
}

// NewStageQuarantineFilter returns a stage that removes quarantined tools from the turn.
// Runs early in the pipeline (before LLM). Zero perceptible latency — a map lookup per tool.
func NewStageQuarantineFilter(q QuarantineChecker) Stage {
	return func(_ context.Context, turn *Turn) error {
		if q == nil {
			return nil
		}
		var kept []Tool
		var filtered []string
		for _, t := range turn.Tools {
			if t.ScanTarget == "" || !q.IsBlocked(t.ScanTarget) {
				kept = append(kept, t)
				continue
			}
			filtered = append(filtered, t.Name)
		}
		if len(filtered) > 0 {
			log.Printf("[security] filtered quarantined tools: %v", filtered)
			turn.SetMeta("security_filtered", filtered)
		}
		turn.Tools = kept
		return nil
	}
}
