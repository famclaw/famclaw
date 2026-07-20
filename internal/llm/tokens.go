package llm

// ComputeMaxResponseTokens returns the safe max_tokens budget for the next
// LLM response, given the prompt size and the model's context window.
// Reserves a safety margin so the model isn't forced to truncate mid-thought
// when reasoning content alone could exceed the cap.
//
//	contextWindow:        total model ctx (e.g. 65536 for a model at q3_k_m)
//	promptTokens:         estimated tokens of the outgoing prompt (system +
//	                      tools + user message). Use EstimatePromptTokens.
//	reservedSafetyMargin: tokens to keep free between max-response and ctx
//	                      end (default 1024)
//
// Returns max_tokens such that:
//
//	promptTokens + max_tokens + safetyMargin <= contextWindow
//
// Floor: 256 — a HARD minimum so a tiny ctx never starves the model.
// Ceiling: contextWindow / 2 — a balance heuristic, NOT a hard cap.
// The ceiling is applied first, then the floor, so the floor always
// wins: on a tiny context where contextWindow/2 < 256, the returned
// budget is 256 even though that exceeds half the context window.
//
// If contextWindow <= 0, returns 2048 (the previous static default).
func ComputeMaxResponseTokens(contextWindow, promptTokens, reservedSafetyMargin int) int {
	if contextWindow <= 0 {
		return 2048
	}
	raw := contextWindow - promptTokens - reservedSafetyMargin
	ceiling := contextWindow / 2
	// Ceiling first, floor second — the 256 floor is the hard guarantee
	// and must not be overridden by the heuristic ceiling.
	if raw > ceiling {
		raw = ceiling
	}
	if raw < 256 {
		raw = 256
	}
	return raw
}

// EstimatePromptTokens returns a rough token estimate for an outgoing prompt.
// Uses a 4-chars-per-token heuristic — close enough for budget computation
// without pulling in a real tokenizer dependency.
//
//	total chars / 4 (rounded up)
func EstimatePromptTokens(messages []Message) int {
	if len(messages) == 0 {
		return 0
	}
	total := 0
	for _, m := range messages {
		total += len(m.Content)
	}
	return (total + 3) / 4
}
