package agentcore

import (
	"fmt"
	"regexp"
	"strings"
)

// ParseSelfSummary extracts the model's self-summary tags from a free-text
// response. The model is instructed (via system prompt — out of scope for this
// phase) to emit three optional XML-like tags before its tool_calls:
//
//	<eval>...</eval>           — what happened with the previous goal
//	<memory>...</memory>       — facts to carry forward about the user request
//	<next_goal>...</next_goal> — what the model intends to do this turn
//
// All three tags are OPTIONAL. Missing tags return "" for that field.
// Tag matching is case-insensitive and tolerant of leading/trailing whitespace
// inside the tag body. Multiple occurrences: only the FIRST occurrence of
// each tag is captured (extra tags ignored).
//
// The function does NOT mutate the input; it returns the three field values.
// Pure function, no side effects.
func ParseSelfSummary(modelOutput string) (eval, memory, nextGoal string) {
	// Regexes are compiled locally rather than held as package-level globals
	// to keep the parser free of global state (repo rule). ParseSelfSummary
	// runs once per tool-loop iteration, so the per-call compile cost is
	// negligible.
	reEval := regexp.MustCompile(`(?is)<eval>(.*?)</eval>`)
	reMemory := regexp.MustCompile(`(?is)<memory>(.*?)</memory>`)
	reNextGoal := regexp.MustCompile(`(?is)<next_goal>(.*?)</next_goal>`)
	if m := reEval.FindStringSubmatch(modelOutput); len(m) > 1 {
		eval = strings.TrimSpace(m[1])
	}
	if m := reMemory.FindStringSubmatch(modelOutput); len(m) > 1 {
		memory = strings.TrimSpace(m[1])
	}
	if m := reNextGoal.FindStringSubmatch(modelOutput); len(m) > 1 {
		nextGoal = strings.TrimSpace(m[1])
	}
	return
}

// HistoryItem is one prior turn in the agent loop, captured as the
// model's OWN short summary (not the raw tool_call / tool_result JSON).
// This is the primitive that prevents reasoning models from hallucinating
// against their own replayed past tool outputs.
type HistoryItem struct {
	StepNum  int      // 1-based, monotonically increasing per conversation
	Eval     string   // model's self-evaluation of the previous goal's outcome
	Memory   string   // model's carry-forward facts about the user's request
	NextGoal string   // what the model intended to do next
	Results  []string // human-readable strings: "[filled e16 = TPA]", "[clicked e21 — page transitioned]", etc.
}

// AgentState carries non-history per-turn context for the rebuild.
type AgentState struct {
	UserRequest string   // verbatim original user message
	Plan        []string // optional checklist, format e.g. "[x] step 1", "[>] step 2", "[ ] step 3"
	StepInfo    string   // optional one-liner like "Step 5 of 25 max. Today: 2026-05-14"
}

// BrowserState carries the freshly-extracted current page snapshot.
// Pass nil when no browser session is active for this turn.
type BrowserState struct {
	URL       string // current URL after redirects
	Title     string // page title
	PageStats string // optional one-liner like "23 interactive, 87 total"
	Snapshot  string // pre-formatted snapshot text (from internal/browser snapshot)
}

// RebuildUserMessage assembles the per-turn user-message-block. The
// returned string is the entire user-message content for the next LLM
// call — the system message stays separate, but no other prior assistant
// or tool messages are sent. This is intentional: the LLM sees only its
// own short summaries (HistoryItem.Eval/Memory/NextGoal/Results) plus
// the current ground truth (browser state).
//
// Format (XML-ish text the model can parse):
//
//	<agent_history>
//	  <step_1>
//	  eval: ...
//	  memory: ...
//	  next_goal: ...
//	  results:
//	  - [filled e16 = "TPA"]
//	  - [clicked e21 — page transitioned]
//	  </step_1>
//	  <step_2>
//	  ...
//	  </step_2>
//	</agent_history>
//
//	<agent_state>
//	<user_request>find me flights TPA→MSY June 10–14</user_request>
//	<plan>
//	[x] navigate to flight search
//	[>] fill destination
//	[ ] set dates
//	</plan>
//	<step_info>Step 3 of 25 max. Today: 2026-05-14</step_info>
//	</agent_state>
//
//	<browser_state>
//	URL: https://www.google.com/travel/flights
//	Title: Google Flights
//	Page stats: 23 interactive, 87 total
//	Interactive elements:
//	<snapshot text here>
//	</browser_state>
//
// Sections are omitted when empty: no <agent_history> if history is empty;
// no <plan> line if state.Plan is empty; no <step_info> if empty;
// no <browser_state> if browser is nil. <agent_state> is always emitted
// because UserRequest is required.
//
// Empty history.Results renders as "results: (none)".
// Empty history.Eval/Memory/NextGoal render as "eval: (n/a)" etc.
// (so the model always sees the same shape — never missing keys).
func RebuildUserMessage(history []HistoryItem, state AgentState, browser *BrowserState) string {
	var b strings.Builder

	// agent_history section — omit when empty
	if len(history) > 0 {
		b.WriteString("<agent_history>\n")
		for _, h := range history {
			fmt.Fprintf(&b, "  <step_%d>\n", h.StepNum)

			eval := h.Eval
			if eval == "" {
				eval = "(n/a)"
			}
			fmt.Fprintf(&b, "  eval: %s\n", eval)

			memory := h.Memory
			if memory == "" {
				memory = "(n/a)"
			}
			fmt.Fprintf(&b, "  memory: %s\n", memory)

			nextGoal := h.NextGoal
			if nextGoal == "" {
				nextGoal = "(n/a)"
			}
			fmt.Fprintf(&b, "  next_goal: %s\n", nextGoal)

			if len(h.Results) == 0 {
				b.WriteString("  results: (none)\n")
			} else {
				b.WriteString("  results:\n")
				for _, r := range h.Results {
					fmt.Fprintf(&b, "  - %s\n", r)
				}
			}

			fmt.Fprintf(&b, "  </step_%d>\n", h.StepNum)
		}
		b.WriteString("</agent_history>\n")
		b.WriteString("\n")
	}

	// agent_state section — always emitted
	b.WriteString("<agent_state>\n")
	fmt.Fprintf(&b, "<user_request>%s</user_request>\n", state.UserRequest)
	if len(state.Plan) > 0 {
		b.WriteString("<plan>\n")
		for _, step := range state.Plan {
			b.WriteString(step)
			b.WriteString("\n")
		}
		b.WriteString("</plan>\n")
	}
	if state.StepInfo != "" {
		fmt.Fprintf(&b, "<step_info>%s</step_info>\n", state.StepInfo)
	}
	b.WriteString("</agent_state>\n")

	// browser_state section — omit when nil
	if browser != nil {
		b.WriteString("\n")
		b.WriteString("<browser_state>\n")
		fmt.Fprintf(&b, "URL: %s\n", browser.URL)
		fmt.Fprintf(&b, "Title: %s\n", browser.Title)
		if browser.PageStats != "" {
			fmt.Fprintf(&b, "Page stats: %s\n", browser.PageStats)
		}
		if browser.Snapshot != "" {
			b.WriteString("Interactive elements:\n")
			b.WriteString(browser.Snapshot)
			b.WriteString("\n")
		}
		b.WriteString("</browser_state>\n")
	}

	return b.String()
}
