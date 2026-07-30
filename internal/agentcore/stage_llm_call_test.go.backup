package agentcore

import (
	"reflect"
	"testing"
)

func TestToolsToLLMDefs_ToolNameNarrowing(t *testing.T) {
	tests := []struct {
		name     string
		tool     Tool
		wantName string
	}{
		{
			name:     "builtin prefix is stripped",
			tool:     Tool{Name: "builtin__web_search", Description: "web search tool", InputSchema: map[string]any{"type": "object"}},
			wantName: "web_search",
		},
		{
			name:     "mcp namespaced name is unchanged",
			tool:     Tool{Name: "mcp__weather__forecast", Description: "weather forecast", InputSchema: map[string]any{"type": "object"}},
			wantName: "mcp__weather__forecast",
		},
		{
			name:     "unprefixed name is unchanged",
			tool:     Tool{Name: "spawn_agent", Description: "spawn a sub-agent", InputSchema: map[string]any{"type": "object"}},
			wantName: "spawn_agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defs := toolsToLLMDefs([]Tool{tt.tool})
			if len(defs) != 1 {
				t.Fatalf("got %d defs, want 1", len(defs))
			}
			got := defs[0]
			if got.Type != "function" {
				t.Errorf("Type = %q, want %q", got.Type, "function")
			}
			if got.Function.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Function.Name, tt.wantName)
			}
			if got.Function.Description != tt.tool.Description {
				t.Errorf("Description = %q, want %q", got.Function.Description, tt.tool.Description)
			}
			if !reflect.DeepEqual(got.Function.Parameters, tt.tool.InputSchema) {
				t.Errorf("Parameters = %v, want %v", got.Function.Parameters, tt.tool.InputSchema)
			}
		})
	}
}
