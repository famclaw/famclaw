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
		{"bare read-only web_search", "web_search", agentcore.ToolErrorDisplayReadOnly},
		{"bare read-only web_fetch", "web_fetch", agentcore.ToolErrorDisplayReadOnly},
		{"namespaced read-only web_search", "builtin__web_search", agentcore.ToolErrorDisplayReadOnly},
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
			result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, map[string]bool{tc.emitted: true}, map[string]bool{}, time.Now())
			if !ok {
				t.Fatal("tool not dispatched")
			}
			if result != tc.want {
				t.Errorf("display = %q, want %q", result, tc.want)
			}
		})
	}
}

// TestSubagentBareNameNormalizedForToolErrorDisplay proves a bare-form
// "web_search" emitted by the LLM is normalized to the namespaced registry
// form before dispatch and before ToolErrorDisplay. Regression: dispatch
// had no bare-name normalization step (unlike the main tool loop), so with
// an allowlist keyed on the namespaced name the bare call was rejected as
// an unknown tool, and a bare web_search failure could never reach
// ToolErrorDisplay's builtin__web_search-specific safe-marker branch.
func TestSubagentBareNameNormalizedForToolErrorDisplay(t *testing.T) {
	marker := agentcore.WebSearchMarkerUnavailable
	var dispatchedAs string
	deps := ExecutorDeps{
		// Keyed on both forms, exactly as agent.handleSpawnAgent builds it.
		BuiltinReadOnly: map[string]bool{"builtin__web_search": true, "web_search": true},
	}
	deps.BuiltinHandler = func(_ context.Context, name string, _ map[string]any) (string, error) {
		dispatchedAs = name
		return marker, errors.New("boom")
	}

	call := llm.ToolCall{Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{}}}
	// The allowlist is keyed on the namespaced registry name, exactly as
	// filterBuiltinDefs produces it from the parent's namespaced defs.
	allowedBuiltins := map[string]bool{"builtin__web_search": true}
	result, ok := dispatchSubagentTool(context.Background(), call, deps, map[string]bool{}, allowedBuiltins, map[string]bool{}, time.Now())
	if !ok {
		t.Fatal("bare web_search was not dispatched to the builtin handler; want it normalized and allowed")
	}
	if dispatchedAs != "builtin__web_search" {
		t.Errorf("handler dispatched as %q, want normalized %q", dispatchedAs, "builtin__web_search")
	}
	if result != marker {
		t.Errorf("display = %q, want specific web_search marker %q", result, marker)
	}
}
