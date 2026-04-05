package toolreg

import (
	"strings"
	"testing"
)

func TestTokenBudget(t *testing.T) {
	tests := []struct {
		ctxWindow int
		want      int
	}{
		{128000, 0},    // unlimited
		{32000, 8000},  // ~40 schemas
		{8000, 1500},   // ~7-8 schemas
		{4096, 500},    // ~2-3 schemas
		{2048, 200},    // ~1 schema
	}
	for _, tt := range tests {
		got := TokenBudget(tt.ctxWindow)
		if got != tt.want {
			t.Errorf("TokenBudget(%d) = %d, want %d", tt.ctxWindow, got, tt.want)
		}
	}
}

func TestEstimateToolTokens(t *testing.T) {
	tool := &Tool{
		Name:        "mcp__weather__forecast",
		Description: "Get weather forecast for a location",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string"},
				"units":    map[string]any{"type": "string"},
			},
		},
	}

	tokens := EstimateToolTokens(tool)
	if tokens <= 0 {
		t.Errorf("EstimateToolTokens should be positive, got %d", tokens)
	}
	// Should be reasonable: name(~6) + desc(~9) + overhead(20) + props(2*15) = ~65
	if tokens > 200 {
		t.Errorf("EstimateToolTokens seems too high: %d", tokens)
	}
}

func TestSelectBasic(t *testing.T) {
	r := New()
	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	// Parent with large context — gets everything
	tools := r.Select(SelectOptions{
		Role:          "parent",
		ContextWindow: 128000,
	})
	if len(tools) != 5 {
		t.Errorf("parent+unlimited: expected 5 tools, got %d", len(tools))
	}

	// Child with large context
	tools = r.Select(SelectOptions{
		Role:          "child",
		ContextWindow: 128000,
	})
	if len(tools) != 3 {
		t.Errorf("child+unlimited: expected 3 tools, got %d", len(tools))
	}
}

func TestSelectSkillScoped(t *testing.T) {
	r := New()
	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	// Only weather tools allowed by active skill
	tools := r.Select(SelectOptions{
		Role:          "parent",
		SkillTools:    []string{"mcp__weather__forecast", "mcp__weather__alerts"},
		ContextWindow: 128000,
	})
	if len(tools) != 2 {
		t.Errorf("skill-scoped: expected 2 tools, got %d", len(tools))
	}
}

func TestSelectTokenBudget(t *testing.T) {
	r := New()
	// Register many tools to exceed budget
	for i := 0; i < 50; i++ {
		r.Register(&Tool{
			Name:        ToolName("mcp", "server", string(rune('a'+i%26))+string(rune('0'+i/26))),
			Description: "A tool that does something useful for testing purposes with a decent description",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"param1": map[string]any{"type": "string"},
					"param2": map[string]any{"type": "number"},
				},
			},
		})
	}

	// Tiny context — should get very few tools
	tools := r.Select(SelectOptions{
		Role:          "parent",
		ContextWindow: 2048,
	})
	if len(tools) >= 50 {
		t.Errorf("tiny context: expected fewer than 50 tools, got %d", len(tools))
	}
	if len(tools) == 0 {
		t.Error("should get at least some tools even with tiny context")
	}
}

func TestToolIndex(t *testing.T) {
	tools := []*Tool{
		{Name: "weather", Description: "Get weather"},
		{Name: "calc", Description: "Calculate"},
	}

	idx := ToolIndex(tools)
	if !strings.Contains(idx, "weather: Get weather") {
		t.Errorf("ToolIndex missing weather: %q", idx)
	}
	if !strings.Contains(idx, "calc: Calculate") {
		t.Errorf("ToolIndex missing calc: %q", idx)
	}
}
