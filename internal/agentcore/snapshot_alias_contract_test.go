package agentcore

import (
	"errors"
	"testing"
)

func TestUnsuccessfulCallIndexSnapshotIsolation(t *testing.T) {
	original := map[string]any{"nested": map[string]any{"value": "before"}}
	results := []ToolResult{{ToolName: "tool", Args: original, Error: errors.New("failed"), Disposition: DispositionAttempted, Outcome: OutcomeFailure}}
	idx := newUnsuccessfulCallIndex()
	if _, found := idx.find("tool", map[string]any{"nested": map[string]any{"value": "before"}}, results); !found {
		t.Fatal("original arguments did not match")
	}
	results[0].Args["nested"].(map[string]any)["value"] = "result changed"
	if got := original["nested"].(map[string]any)["value"]; got != "before" {
		t.Errorf("result mutation changed external argument: %v", got)
	}
	original["nested"].(map[string]any)["value"] = "external changed"
	if got := results[0].Args["nested"].(map[string]any)["value"]; got != "result changed" {
		t.Errorf("external mutation changed result snapshot: %v", got)
	}
}
