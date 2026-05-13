package agent

// computeHeadBudget returns the maximum bytes a single tool result's head
// slice should occupy when the result is being spilled to the toolcache.
// See spec §6.
//
//	budget = head_share * (n_ctx * (1 - margin) - non_droppable - response_reserve)
//	floor  = 0.5 * n_ctx in bytes (safety floor — never exceed)
//
// Returns bytes. Conversion uses 4 chars/token (SimpleEstimator's
// heuristic) which matches what compress.Compress uses for budget math.
func computeHeadBudget(a *Agent) int {
	const (
		bytesPerToken    = 4
		responseReserve  = 1024 // tokens reserved for response
		nonDroppableEst  = 1500 // tokens for system prompt + last K turns
		estimatorMargin  = 0.15
		headShare        = 0.5
		safetyFloorRatio = 0.5
	)

	nCtx := 0
	if a != nil && a.cfg != nil {
		nCtx = a.cfg.LLM.MaxContextTokens
	}
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

	floorBytes := int(safetyFloorRatio * float64(nCtx) * bytesPerToken)
	if budgetBytes > floorBytes {
		budgetBytes = floorBytes
	}
	if budgetBytes < 512 {
		budgetBytes = 512 // never starve the model
	}
	return budgetBytes
}
