package agent

// Absolute bounds for the head budget. Extracted to package-level so that
// tests can reference them by name rather than restating literal byte values.
const (
	// minHeadBytes is the absolute floor: even on the smallest supported
	// context the model gets at least this many bytes of preview.
	minHeadBytes = 512
	// maxHeadBytes is the absolute ceiling: the head is a preview, not a
	// dump, so it never exceeds 64 KB regardless of how large the
	// configured context window is.
	maxHeadBytes = 64 * 1024
)

// HeadBudgetForContext returns the maximum bytes a single tool result's head
// slice may occupy before it spills into the toolcache. It is the spill
// threshold AND the preview size the LLM receives inline.
// See spec §6.
//
//	budget = headShare * (n_ctx * (1 - margin) - non_droppable - response_reserve)
//	budget = clamp(budget, minHeadBytes, maxHeadBytes)
//
// The resulting budget scales with the context window so that larger
// models get larger previews, but absolute floor/ceiling bounds keep the
// value sane at extreme context sizes.
//
// This is exported as a pure function of the context size so that tests in
// other packages (e.g. toolcache) can call the same helper the production
// path uses, rather than duplicating the derived value as a magic number.
//
// Returns bytes. Conversion uses 4 chars/token (SimpleEstimator's
// heuristic) which matches what compress.Compress uses for budget math.
func HeadBudgetForContext(nCtx int) int {
	const (
		bytesPerToken   = 4
		responseReserve = 1024 // tokens reserved for response
		nonDroppableEst = 1500 // tokens for system prompt + last K turns
		estimatorMargin = 0.15
		// headShare is the fraction of the usable context window a single
		// tool result may occupy before it spills. Lowered from 0.50 (50%
		// of the window ≈ 213 KB at the default 128K-token context — which
		// never engaged in production because no real tool result is that
		// large) to 0.02 (2%) so realistic results — fetched web pages,
		// file reads, search output — spill while small inline results
		// stay inline.
		headShare = 0.02
	)

	if nCtx <= 0 {
		nCtx = 4096
	}

	usableTokens := float64(nCtx)*(1-estimatorMargin) -
		float64(nonDroppableEst) - float64(responseReserve)
	if usableTokens < 0 {
		// Degenerate case (tiny n_ctx) — give the model 10% of the window
		// rather than zero so research at least returns something.
		usableTokens = float64(nCtx) * 0.1
	}
	budgetTokens := int(usableTokens * headShare)
	budgetBytes := budgetTokens * bytesPerToken

	// Floor: ensure a minimally useful preview even on tiny contexts.
	if budgetBytes < minHeadBytes {
		budgetBytes = minHeadBytes
	}
	// Ceiling: cap the head regardless of context size so it stays a
	// preview, not a context-dump.
	if budgetBytes > maxHeadBytes {
		budgetBytes = maxHeadBytes
	}
	return budgetBytes
}

// computeHeadBudget returns the head budget for the agent's configured
// context window. Delegates to the exported HeadBudgetForContext.
func computeHeadBudget(a *Agent) int {
	nCtx := 0
	if a != nil && a.cfg != nil {
		nCtx = a.cfg.LLM.MaxContextTokens
	}
	return HeadBudgetForContext(nCtx)
}
