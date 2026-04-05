package toolreg

import (
	"sync"
	"testing"
)

func sampleTools() []*Tool {
	return []*Tool{
		{Name: "mcp__weather__forecast", Description: "Get weather", Source: "mcp", ServerName: "weather", Roles: nil},
		{Name: "mcp__weather__alerts", Description: "Get alerts", Source: "mcp", ServerName: "weather", Roles: []string{"parent"}},
		{Name: "mcp__calc__add", Description: "Add numbers", Source: "mcp", ServerName: "calc", Roles: nil},
		{Name: "builtin__spawn_agent", Description: "Spawn subagent", Source: "builtin", Roles: []string{"parent"}},
		{Name: "plugin__search__web", Description: "Web search", Source: "plugin", Roles: []string{"parent", "child"}},
	}
}

func TestRegistryBasicOps(t *testing.T) {
	r := New()

	if r.Len() != 0 {
		t.Errorf("new registry should be empty, got %d", r.Len())
	}

	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	if r.Len() != 5 {
		t.Errorf("expected 5 tools, got %d", r.Len())
	}

	// Get existing
	tool := r.Get("mcp__weather__forecast")
	if tool == nil || tool.Description != "Get weather" {
		t.Errorf("Get failed: %v", tool)
	}

	// Get missing
	if r.Get("nonexistent") != nil {
		t.Error("expected nil for missing tool")
	}

	// Has
	if !r.Has("builtin__spawn_agent") {
		t.Error("expected Has to return true")
	}
	if r.Has("missing") {
		t.Error("expected Has to return false")
	}

	// Remove
	r.Remove("builtin__spawn_agent")
	if r.Len() != 4 {
		t.Errorf("after remove: expected 4, got %d", r.Len())
	}
	if r.Has("builtin__spawn_agent") {
		t.Error("tool should be removed")
	}
}

func TestFilterByRole(t *testing.T) {
	r := New()
	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	// Parent sees everything
	parentTools := r.FilterByRole("parent")
	if len(parentTools) != 5 {
		t.Errorf("parent should see 5 tools, got %d", len(parentTools))
	}

	// Child sees: forecast (no roles = all), calc (no roles), search (explicit child)
	childTools := r.FilterByRole("child")
	if len(childTools) != 3 {
		t.Errorf("child should see 3 tools, got %d", len(childTools))
		for _, tool := range childTools {
			t.Logf("  child sees: %s", tool.Name)
		}
	}

	// Unknown role sees only tools with empty Roles
	unknownTools := r.FilterByRole("unknown")
	if len(unknownTools) != 2 {
		t.Errorf("unknown should see 2 tools (no role restriction), got %d", len(unknownTools))
	}
}

func TestFilterBySkills(t *testing.T) {
	r := New()
	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	result := r.FilterBySkills([]string{"mcp__weather__forecast", "mcp__calc__add"})
	if len(result) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result))
	}

	// Empty allowlist returns nothing
	result = r.FilterBySkills(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 tools for nil allowlist, got %d", len(result))
	}
}

func TestFilterBySource(t *testing.T) {
	r := New()
	for _, tool := range sampleTools() {
		r.Register(tool)
	}

	mcpTools := r.FilterBySource("mcp")
	if len(mcpTools) != 3 {
		t.Errorf("expected 3 mcp tools, got %d", len(mcpTools))
	}

	builtinTools := r.FilterBySource("builtin")
	if len(builtinTools) != 1 {
		t.Errorf("expected 1 builtin tool, got %d", len(builtinTools))
	}
}

func TestToolName(t *testing.T) {
	tests := []struct {
		source, server, name, want string
	}{
		{"mcp", "weather", "forecast", "mcp__weather__forecast"},
		{"builtin", "", "spawn_agent", "builtin__spawn_agent"},
		{"plugin", "search", "web", "plugin__search__web"},
	}

	for _, tt := range tests {
		got := ToolName(tt.source, tt.server, tt.name)
		if got != tt.want {
			t.Errorf("ToolName(%q,%q,%q) = %q, want %q", tt.source, tt.server, tt.name, got, tt.want)
		}
	}
}

func TestRegistryReplaceExisting(t *testing.T) {
	r := New()
	r.Register(&Tool{Name: "test", Description: "v1"})
	r.Register(&Tool{Name: "test", Description: "v2"})

	if r.Len() != 1 {
		t.Errorf("expected 1 after replace, got %d", r.Len())
	}
	if r.Get("test").Description != "v2" {
		t.Error("expected updated description")
	}
}

func TestRegistryConcurrency(t *testing.T) {
	r := New()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Register(&Tool{Name: ToolName("mcp", "server", string(rune('a'+i%26)))})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.List()
			r.FilterByRole("parent")
			r.Len()
		}()
	}

	wg.Wait()

	// Should not panic or race
	if r.Len() == 0 {
		t.Error("expected tools after concurrent registration")
	}
}
