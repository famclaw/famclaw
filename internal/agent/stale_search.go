package agent

import (
	"context"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// This file implements the provider-neutral tool-reconsideration guards:
// a single one-time re-prompt that fires when the model's final output
// claims inability and zero tools were attempted this turn, and the
// matching "try again" prelude when the user explicitly asks to retry and
// a failure-claim sits in the trailing history.
//
// Both paths are generic: they match on ANY actually offered tool (built-in
// or opaque MCP), never on a specific tool name or provider. After ANY
// recorded execution outcome (success, error, uncertain mutation, skipped,
// pending, or denied) the recorded outcome is preserved and no automatic
// second pipeline run occurs. Tool receipts are not truth or authority
// certification; read-only hints and successful output never certify a
// result.
//
// Fix 1 (isRetryIntent + trailingHistoryHasFailureClaim): on an explicit
// "try again" request, append a point-in-time system directive so the model
// reconsiders the request with the tools actually offered this turn.
//
// Fix 3 (shouldFreshSearchRetry): cross-turn near-duplicate backstop. If
// tools were offered and the final output still claims inability without a
// recorded tool call this turn, re-prompt ONCE. Bounded to one retry; a
// second failed attempt is delivered as-is (logged) rather than looped.
// This also covers the very first turn (no "try again" intent), which is
// where the original failure actually started.

// freshSearchDirective is the point-in-time instruction appended to the
// conversation when a stale "couldn't check/access" answer must be re-run. It
// is sent as a system-role message so StageCompress pins it (system messages
// are never dropped) and it reaches the wire as the LAST instruction the model
// sees, steering it to reconsider the request with the tools ACTUALLY offered
// this turn — never forcing a specific provider — instead of reusing the prior
// answer. The retry predicate only fires when zero tools were attempted this
// turn, so the recorded execution outcome from any tool (built-in or MCP)
// is preserved; read-only hints and tool results never certify truth.
const freshSearchDirective = "Your previous answer claimed you could not check, search, or access this. Reconsider the request using the tools actually offered this turn, preserving any recorded execution outcome. If no offered tool can resolve it, say so plainly rather than guessing; do not fabricate results."

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
		// Search-specific inability markers. A bare "without being able to"
		// false-positives on generic refusals ("I can't do that without
		// being able to access the database"), so each variant must
		// reference the search/lookup/check ability itself.
		"without being able to search", "without being able to check", "without being able to look",
		// "not be able to" variants — already tied to the search verbs
		"not be able to check", "not be able to search",
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

// hasAnyToolOffered reports whether any tool at all is in the turn's tool
// allowlist (i.e. was actually offered to the LLM this turn) — builtins or
// opaque MCP tools alike. Both the prelude retry-intent path and the
// post-output backstop reconsideration use this single generic check; no
// tool-name-based provider routing prerequisite remains in either path.
func hasAnyToolOffered(turn *agentcore.Turn) bool {
	return len(turn.Tools) > 0
}

// shouldFreshSearchRetry reports whether the cross-turn near-duplicate guard
// (Fix 3) should fire: tools are offered this turn, the model's final output
// still claims inability, and NO tool call was recorded this turn. The
// one-time re-prompt must only fire when nothing was attempted: after ANY
// executed tool (success, error, uncertain mutation, skipped, pending, or
// denied) the recorded execution outcome is preserved and an automatic second
// pipeline run would re-execute an already-attempted action or discard that
// receipt.
func (a *Agent) shouldFreshSearchRetry(turn *agentcore.Turn) bool {
	return hasAnyToolOffered(turn) &&
		hasSearchFailureClaim(turn.Output) &&
		len(turn.ToolCalls) == 0
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
