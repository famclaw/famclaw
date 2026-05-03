// Package prompt assembles the FamClaw system prompt from a set of
// conditional components. Each component returns (text, included);
// excluded components contribute nothing. Component order is fixed
// in Build.
package prompt

import (
	"strings"

	"github.com/famclaw/famclaw/internal/config"
)

// BuildContext is the input to every component. Components only
// read from it — never mutate.
type BuildContext struct {
	Cfg          *config.Config     // family config (users list, etc.)
	User         *config.UserConfig // the user this prompt is for
	Gateway      string             // "telegram" | "discord" | "web" | ""
	Skills       []string           // skill names loaded for this user; can be empty
	OAuth        bool               // true if the LLM endpoint uses Anthropic OAuth
	HardBlocked  []string           // hard-blocked policy categories for this user
	BuiltinTools []string           // builtin tool bare names (e.g. "spawn_agent", "web_fetch")
}

// component returns (text, included). Empty text or included=false → skipped.
type component func(BuildContext) (string, bool)

// Build runs every component in order and joins included results
// with a blank line. Component order is fixed for token economy:
// identity first (always), then progressively narrower context.
func Build(ctx BuildContext) string {
	components := []component{
		oauthPrefixComponent, // first — Anthropic OAuth requirement
		identityComponent,
		userComponent,
		familyComponent,
		ageComponent,
		capabilitiesComponent,
		skillsComponent,
		policyComponent,
		approvalsComponent,
		gatewayComponent,
		outputComponent,
		memoryComponent,
	}
	var parts []string
	for _, c := range components {
		if text, ok := c(ctx); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// identityComponent — always present. Establishes who FamClaw is.
func identityComponent(_ BuildContext) (string, bool) {
	return "You are FamClaw, a private family AI assistant. " +
		"Every message routes through a policy engine before reaching you, " +
		"and every response is scanned before reaching the user. " +
		"You can hold normal conversations, answer questions, and use any " +
		"tools that have been provided to you in this session.", true
}
