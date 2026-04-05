package toolreg

// SelectOptions controls tool selection behavior.
type SelectOptions struct {
	Role          string   // user role for filtering
	SkillTools    []string // tools declared by active skills (empty = all)
	ContextWindow int      // context window in tokens
}

// TokenBudget returns the maximum tokens for tool schemas based on context window.
func TokenBudget(contextWindow int) int {
	switch {
	case contextWindow >= 128000:
		return 0 // unlimited — context is huge
	case contextWindow >= 32000:
		return 8000 // ~40 schemas
	case contextWindow >= 8000:
		return 1500 // ~7-8 schemas
	case contextWindow >= 4000:
		return 500 // ~2-3 schemas
	default:
		return 200 // ~1 schema
	}
}

// EstimateToolTokens estimates how many tokens a tool's schema uses.
// Rough estimate: name + description + schema properties.
func EstimateToolTokens(t *Tool) int {
	base := len(t.Name)/4 + len(t.Description)/4 + 20 // name, desc, overhead
	// Rough estimate for schema properties
	if t.InputSchema != nil {
		if props, ok := t.InputSchema["properties"].(map[string]any); ok {
			base += len(props) * 15 // ~15 tokens per property
		}
	}
	return base
}

// Select returns tools appropriate for the given options, staying within token budget.
// Applies: role filter → skill scope → token budget check.
func (r *Registry) Select(opts SelectOptions) []*Tool {
	// Strategy 1: Role filter
	tools := r.FilterByRole(opts.Role)

	// Strategy 2: Skill-scoped filter (if skills declared tools)
	if len(opts.SkillTools) > 0 {
		allowed := make(map[string]bool, len(opts.SkillTools))
		for _, name := range opts.SkillTools {
			allowed[name] = true
		}
		var filtered []*Tool
		for _, t := range tools {
			if allowed[t.Name] {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	// Check token budget
	budget := TokenBudget(opts.ContextWindow)
	if budget == 0 {
		return tools // unlimited
	}

	// If within budget, return all
	total := 0
	for _, t := range tools {
		total += EstimateToolTokens(t)
	}
	if total <= budget {
		return tools
	}

	// Over budget — trim to fit (keep highest priority = first registered)
	var selected []*Tool
	used := 0
	for _, t := range tools {
		cost := EstimateToolTokens(t)
		if used+cost > budget {
			break
		}
		selected = append(selected, t)
		used += cost
	}
	return selected
}

// ToolIndex returns a compact representation of tools (name + description only)
// for two-pass selection. Uses ~5 tokens per tool.
func ToolIndex(tools []*Tool) string {
	var s string
	for _, t := range tools {
		s += t.Name + ": " + t.Description + "\n"
	}
	return s
}
