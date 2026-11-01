package agent

import (
	"context"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// This file implements the two hard guards for the "stale web-search" failure
// documented in internal/agentcore and the butler-stale-search root-cause
// report. The failure signature: the LLM emits "I can't access / check /
// search X" WITHOUT calling web_search, then re-emits the same stale claim
// verbatim when the user says "try search again" — because the prior failed
// assistant message sits in the conversation history and the model reuses it
// instead of running a fresh search.
//
// Fix 1 (isRetryIntent + trailingHistoryHasFailureClaim): on an explicit
// "try again"/"search again" request, append a point-in-time system directive
// so the model is forced to run a NEW search this turn.
//
// Fix 3 (shouldFreshSearchRetry): cross-turn near-duplicate backstop. If
// web_search was available and the final output still claims it couldn't
// check/search WITHOUT a real web_search ToolResult this turn, re-prompt ONCE.
// Bounded to one retry; a second failed attempt is delivered as-is (logged)
// rather than looped. This also covers the very first turn (no "try again"
// intent), which is where the original failure actually started.

// freshSearchDirective is the point-in-time instruction appended to the
// conversation when a stale "couldn't check/search" answer must be re-run. It
// is sent as a system-role message so StageCompress pins it (system messages
// are never dropped) and it reaches the wire as the LAST instruction the model
// sees, steering it to run a NEW search instead of reusing the prior answer.
const freshSearchDirective = "The web search tool is available now. Your previous answer said you could not check or search, and that is out of date. Call web_search for this request and answer ONLY from this turn's fresh results — do not reuse the earlier answer. If the new search still returns nothing useful, say so plainly rather than guessing."

// isRetryIntent reports whether the user is explicitly asking to try a
// previous attempt again. Deliberately narrow — only strong "do it again"
// phrasings — so it does not fire on unrelated "try X" messages (e.g. "try a
// different tone").
func isRetryIntent(msg string) bool {
	lower := strings.ToLower(msg)
	markers := []string{
		"try again", "try it again", "try that again", "try one more time", "try once more", "one more time",
		"try search", "try searching", "try a search", "try looking", "try fetching", "try fetch",
		"search again", "search it again", "run the search", "look it up again",
		"look again", "check again", "check it again", "retry", "re-try", "do it again",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// hasSearchFailureClaim reports whether a model output claims it could not
// check or search something — the exact wording the stale-search failure
// produces. Deliberately narrower than generic "I can't help" so it does not
// fire on legitimate "searched and found nothing" answers (those record a real
// web_search result and therefore never reach the retry).
func hasSearchFailureClaim(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"i can't access", "i cannot access", "i don't have access", "don't have access to",
		"i'm unable to check", "i am unable to check", "i couldn't check", "i can't check",
		"i'm unable to search", "i am unable to search", "i couldn't search", "i can't search",
		"i'm not able to search", "i don't have the tool", "there's no tool", "i don't have a tool",
		"i can't look that up", "i cannot look that up", "i'm unable to look",
		"without being able to", "not be able to check", "not be able to search",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// trailingHistoryHasFailureClaim reports whether any of the last few history
// messages (assistant or user) carries a search-failure claim — the "previously
// said it couldn't check" half of the stale-search failure.
func trailingHistoryHasFailureClaim(history []*store.Message) bool {
	if len(history) == 0 {
		return false
	}
	start := len(history) - 4
	if start < 0 {
		start = 0
	}
	for _, m := range history[start:] {
		if hasSearchFailureClaim(m.Content) {
			return true
		}
	}
	return false
}

// webSearchAvailable reports whether web_search is in the turn's tool allowlist
// (i.e. it was actually offered to the LLM this turn).
func webSearchAvailable(turn *agentcore.Turn) bool {
	for _, t := range turn.Tools {
		if t.Name == "builtin__web_search" || t.Name == "web_search" {
			return true
		}
	}
	return false
}

// recordedWebSearchThisTurn reports whether a web_search ToolResult was
// actually recorded this turn (canonical "builtin__web_search" name, with the
// bare name checked defensively).
func recordedWebSearchThisTurn(results []agentcore.ToolResult) bool {
	for _, tr := range results {
		if tr.ToolName == "builtin__web_search" || tr.ToolName == "web_search" {
			return true
		}
	}
	return false
}

// shouldFreshSearchRetry reports whether the cross-turn near-duplicate guard
// (Fix 3) should fire: web_search is available this turn, the model's final
// output still claims it couldn't check/search, and no web_search ToolResult
// was actually recorded this turn.
func (a *Agent) shouldFreshSearchRetry(turn *agentcore.Turn) bool {
	return webSearchAvailable(turn) &&
		hasSearchFailureClaim(turn.Output) &&
		!recordedWebSearchThisTurn(turn.ToolCalls)
}

// gateOutput runs the output policy gate against turn.Output. When the output
// is blocked or redacted it persists a fallback message and returns a block
// Response plus done=true so Chat can short-circuit. On an allowed output it
// sanitizes turn.Output in place and returns done=false. Extracted from the
// inline gate so the fresh-search retry can re-run the gate against the second
// attempt without duplicating the gate logic.
func (a *Agent) gateOutput(ctx context.Context, turn *agentcore.Turn) (*Response, bool) {
	if a.evaluator == nil {
		return nil, false
	}
	final, allowed, gateErr := EvaluateAndApply(ctx, a.evaluator, turn.Output,
		policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup}, "")
	if gateErr != nil || !allowed {
		if gateErr != nil {
			log.Printf("[agent][%s] output gate error (treating as block): %v", a.user.Name, gateErr)
		}
		blocked := "I'm unable to send this response right now."
		if dbErr := a.db.SaveMessage(a.convID, a.user.Name, "assistant", blocked, string(turn.Category), "block", a.gatewayForSave()); dbErr != nil {
			log.Printf("[agent][%s] save output-gated response: %v", a.user.Name, dbErr)
		}
		return &Response{
			Content:      blocked,
			PolicyAction: "block",
			Category:     string(turn.Category),
		}, true
	}
	turn.Output = sanitizeModelResponse(final)
	return nil, false
}
