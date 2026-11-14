package subagent

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
)

func TestSubagentBuiltinFailureClassification(t *testing.T) {
	const name = "builtin__operation"
	const secret = "fake-private-host-or-path"
	for _, tc := range []struct {
		name     string
		cause    error
		class    string
		readOnly bool
	}{
		{"deadline", context.DeadlineExceeded, "Error class: timeout.", false},
		{"permission", fs.ErrPermission, "Error class: permission denied.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			deps := ExecutorDeps{BuiltinReadOnly: map[string]bool{name: tc.readOnly}, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				calls++
				return secret, fmt.Errorf("%s: %w", secret, tc.cause)
			}}
			out, ok := dispatchSubagentTool(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: name}}, deps, nil, map[string]bool{name: true}, nil, time.Now())
			if !ok || calls != 1 || !strings.Contains(out, tc.class) || strings.Contains(out, secret) {
				t.Fatalf("out=%q ok=%t calls=%d", out, ok, calls)
			}
			if !tc.readOnly && !strings.Contains(out, "some changes may still have occurred") {
				t.Fatalf("write uncertainty lost: %q", out)
			}
			if tc.readOnly && !strings.Contains(out, "effects are not verified") {
				t.Fatalf("read-only caveat lost: %q", out)
			}
		})
	}
}
