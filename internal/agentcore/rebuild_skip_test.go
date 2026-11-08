package agentcore

import (
	"errors"
	"strings"
	"testing"
)

func TestRebuildResultLineSkippedState(t *testing.T) {
	const secret = "fake-private-detail"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"intentional skip", nil, "skip=skipped; not run"},
		{"skipped with error", errors.New(secret), "err=skipped; not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := rebuildResultLine(ToolResult{ToolName: "builtin__browser_extract", Args: map[string]any{"secret": secret}, Error: tc.err, Disposition: DispositionSkipped})
			if !strings.Contains(line, tc.want) || strings.Contains(line, secret) || strings.Contains(line, "ok bytes=") {
				t.Errorf("history line = %q, want %q and no private detail or success", line, tc.want)
			}
		})
	}
}
