// Package toolreg provides a unified tool registry for FamClaw.
// All tools (MCP, builtin, plugin) are registered here with schemas,
// enabling role-based filtering and skill-scoped selection.
package toolreg

import (
	"fmt"
	"sync"
)

// Tool describes a callable tool available to the LLM.
type Tool struct {
	Name        string         `json:"name"`         // namespaced: "mcp__weather__forecast"
	Description string         `json:"description"`  // one-line description
	InputSchema map[string]any `json:"input_schema"` // JSON Schema for parameters
	Source      string         `json:"source"`        // "mcp", "builtin", "plugin"
	ServerName  string         `json:"server_name"`   // which MCP server owns this (empty for builtins)
	Roles       []string       `json:"roles"`         // allowed roles (empty = all)
}

// AllowedForRole returns true if the tool is available for the given role.
func (t *Tool) AllowedForRole(role string) bool {
	if len(t.Roles) == 0 {
		return true
	}
	for _, r := range t.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Registry is a thread-safe store of all available tools.
type Registry struct {
	tools map[string]*Tool
	mu    sync.RWMutex
}

// New creates an empty tool registry.
func New() *Registry {
	return &Registry{
		tools: make(map[string]*Tool),
	}
}

// Register adds or replaces a tool in the registry.
func (r *Registry) Register(tool *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name] = tool
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Has returns true if the tool exists in the registry.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Remove deletes a tool from the registry.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// List returns all registered tools.
func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// FilterByRole returns tools available to the given role.
func (r *Registry) FilterByRole(role string) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Tool
	for _, t := range r.tools {
		if t.AllowedForRole(role) {
			result = append(result, t)
		}
	}
	return result
}

// FilterBySkills returns tools that belong to the given skill tool names.
// Tools not in the allowlist are excluded.
func (r *Registry) FilterBySkills(toolNames []string) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	allowed := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		allowed[name] = true
	}
	var result []*Tool
	for _, t := range r.tools {
		if allowed[t.Name] {
			result = append(result, t)
		}
	}
	return result
}

// FilterBySource returns tools from the given source (e.g. "mcp", "builtin").
func (r *Registry) FilterBySource(source string) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Tool
	for _, t := range r.tools {
		if t.Source == source {
			result = append(result, t)
		}
	}
	return result
}

// ToolName builds a namespaced tool name.
func ToolName(source, server, name string) string {
	if server == "" {
		return fmt.Sprintf("%s__%s", source, name)
	}
	return fmt.Sprintf("%s__%s__%s", source, server, name)
}
