package agentcore

import (
	"reflect"
	"testing"
)

// TestToolsToLLMDefs_MatchesCapabilitiesPrompt tests that toolsToLLMDefs
// produces LLM-facing tool names that match the capabilities prompt
// by stripping the builtin__ prefix while keeping mcp__ names namespaced
func TestToolsToLLMDefs_MatchesCapabilitiesPrompt(t *testing.T) {
	tests := []struct {
		name           string
		toolName       string
		expectedName   string
		description    string
		inputSchema    map[string]any
	}{
		{
			name:         "builtin prefix stripped for LLM-facing names",
			toolName:     "builtin__web_search",
			expectedName: "web_search",
			description:  "web search tool",
			inputSchema:  map[string]any{"type": "object"},
		},
		{
			name:         "mcp namespaced names preserved",
			toolName:     "mcp__weather__forecast",
			expectedName: "mcp__weather__forecast",
			description:  "weather forecast tool",
			inputSchema:  map[string]any{"type": "object"},
		},
		{
			name:         "unprefixed names unchanged",
			toolName:     "spawn_agent",
			expectedName: "spawn_agent",
			description:  "spawn a sub-agent",
			inputSchema:  map[string]any{"type": "object"},
		},
		{
			name:         "builtin prefix stripped for complex tool",
			toolName:     "builtin__browser_navigate",
			expectedName: "browser_navigate",
			description:  "navigate browser to URL",
			inputSchema:  map[string]any{"type": "object"},
		},
		{
			name:         "mcp namespaced with underscores preserved",
			toolName:     "mcp__some_server__complex_tool",
			expectedName: "mcp__some_server__complex_tool",
			description:  "complex tool with underscores",
			inputSchema:  map[string]any{"type": "object"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := Tool{
				Name:        tt.toolName,
				Description: tt.description,
				InputSchema: tt.inputSchema,
			}
			
			defs := toolsToLLMDefs([]Tool{tool})
			if len(defs) != 1 {
				t.Fatalf("got %d defs, want 1", len(defs))
			}
			
			got := defs[0]
			if got.Type != "function" {
				t.Errorf("Type = %q, want %q", got.Type, "function")
			}
			
			if got.Function.Name != tt.expectedName {
				t.Errorf("Name = %q, want %q", got.Function.Name, tt.expectedName)
			}
			
			if got.Function.Description != tt.description {
				t.Errorf("Description = %q, want %q", got.Function.Description, tt.description)
			}
			
			if !reflect.DeepEqual(got.Function.Parameters, tt.inputSchema) {
				t.Errorf("Parameters = %v, want %v", got.Function.Parameters, tt.inputSchema)
			}
		})
	}
}

// TestToolsToLLMDefs_ExactRequirements tests the exact requirements from user intent
func TestToolsToLLMDefs_ExactRequirements(t *testing.T) {
	// This test verifies the exact behavior described in the user intent:
	// "Make the LLM-facing tool names in toolsToLLMDefs match the capabilities prompt 
	// by stripping the builtin__ prefix while keeping mcp__ names namespaced, 
	// with a table-driven test."
	
	// The function should strip builtin__ prefix for LLM-facing names
	// but keep mcp__ namespaced names intact
	
	tests := []struct {
		name          string
		toolName      string
		shouldStrip   bool
		expectedName  string
	}{
		{
			name:         "builtin__ prefix stripped for LLM names",
			toolName:     "builtin__web_search",
			shouldStrip:  true,
			expectedName: "web_search",
		},
		{
			name:         "mcp__ namespaced names kept intact",
			toolName:     "mcp__weather__forecast",
			shouldStrip:  false,
			expectedName: "mcp__weather__forecast",
		},
		{
			name:         "unprefixed names unchanged",
			toolName:     "echo",
			shouldStrip:  false,
			expectedName: "echo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := Tool{
				Name:        tt.toolName,
				Description: "test tool",
				InputSchema: map[string]any{"type": "object"},
			}

			defs := toolsToLLMDefs([]Tool{tool})
			if len(defs) != 1 {
				t.Fatalf("Expected 1 definition, got %d", len(defs))
			}

			actualName := defs[0].Function.Name
			if actualName != tt.expectedName {
				t.Errorf("Tool name mismatch: got %q, want %q", actualName, tt.expectedName)
			}
		})
	}
}

// Keep the original test for backward compatibility
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
