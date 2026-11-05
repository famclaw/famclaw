package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/llm"
)

// TestSubagentReadOnlyBareNameLookup proves dispatchSubagentTool's per-call
// read-only classification is correct in every name form that can reach it:
// the namespaced registry form ("builtin__web_search") and the bare form a
// local LLM may emit instead ("web_search"). Regression: BuiltinReadOnly was
// once keyed only on the namespaced tool name in agent.handleSpawnAgent, so a
// bare-form lookup silently missed and a read-only tool failure degraded to
// the conservative "some changes may have occurred" display instead of "No
// changes were made."
func TestSubagentReadOnlyBareNameLookup(t *testing.T) {
	// Build the map exactly the way agent.handleSpawnAgent keys it: both
	// the namespaced registry name and its bare form, for ReadOnly tools
	// only (mutating tools are absent, staying potentially mutating).
	registry := []struct {
		name     string
		readOnly bool
	}{
		{"builtin__web_search", true},
		{"builtin__web_fetch", true},
		{"builtin__todo", false},
	}
	readonly := make(map[string]bool, len(registry)*2)
	for _, tool := range registry {
		if tool.readOnly {
			readonly[tool.name] = true
			readonly[strings.TrimPrefix(tool.name, "builtin__")] = true
		}
	}

	for _, tc := range []struct {
		name    string
		emitted string
		want    string
	}{
		{"bare read-only web_search", "web_search", agentcore.ToolErrorDisplayNoChanges},
		{"bare read-only web_fetch", "web_fetch", agentcore.ToolErrorDisplayNoChanges},
		{"namespaced read-only web_search", "builtin__web_search", agentcore.ToolErrorDisplayNoChanges},
		{"bare mutating tool stays conservative", "todo", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
		{"namespaced mutating tool stays conservative", "builtin__todo", agentcore.ToolErrorDisplayChangesMayHaveOccurred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := ExecutorDeps{BuiltinReadOnly: readonly}
			deps.BuiltinHandler = func(context.Context, string, map[string]any) (string, error) {
				return "", errors.New("boom")
			}
			call := llm.ToolCall{Function: llm.ToolCallFunction{Name: tc.emitted, Arguments: map[string]any{}}}
			// The allowlist gate (filterBuiltinDefs) is a separate mechanism;
			// here it is opened for the emitted name so the read-only
			// classification lookup itself is what is under test.
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, map[string]bool{tc.emitted: true}, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if result != tc.want {
				t.Errorf("display = %q, want %q", result, tc.want)
			}
		})
	}
}
