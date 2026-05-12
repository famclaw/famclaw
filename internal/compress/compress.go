package compress

// Message mirrors the conversation message format.
type Message struct {
	Role     string
	Content  string
	Pinned   bool // pinned messages survive truncation
	Prunable bool // prefer-drop messages (typically tool results) — Tier 0 evicts before non-Prunable
}

// Options controls compression behavior.
type Options struct {
	ContextWindow   int            // total context window in tokens
	Estimator       TokenEstimator // token estimator (defaults to SimpleEstimator)
	EstimatorMargin float64        // 0..1 safety buffer for SimpleEstimator inaccuracy; default 0.15 when zero
}

func (o *Options) estimator() TokenEstimator {
	if o.Estimator != nil {
		return o.Estimator
	}
	return &SimpleEstimator{}
}

// Compress reduces the message list to fit within the context window.
// Applies Tier 0 (smart truncation) — drops oldest non-pinned messages.
// Returns the compressed message list.
func Compress(messages []Message, opts Options) []Message {
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 4096
	}
	est := opts.estimator()

	// Calculate total tokens
	total := totalTokens(messages, est)
	budget := int(float64(opts.ContextWindow) * 0.85) // leave 15% for response

	// Apply estimator safety margin. SimpleEstimator is chars/4 — off by
	// 10–20% depending on content. Shrinking the budget by EstimatorMargin
	// absorbs that error so we don't push past n_ctx.
	margin := opts.EstimatorMargin
	if margin == 0 {
		margin = 0.15
	}
	if margin < 0 {
		margin = 0
	}
	if margin >= 1 {
		margin = 0.99
	}
	budget = int(float64(budget) * (1 - margin))

	if total <= budget {
		return messages // fits already
	}

	return truncate(messages, budget, est)
}

// truncate implements Tier 0: drop oldest non-protected messages.
// Two-pass drop: first prefer Prunable (typically tool results) so they
// get evicted before conversation turns; then drop any non-protected if
// still over budget.
// Protected: system prompt (index 0), pinned messages, last 3 messages.
func truncate(messages []Message, budget int, est TokenEstimator) []Message {
	if len(messages) <= 4 {
		return messages // too few to truncate
	}

	// Always keep: system prompt (first), last 3 messages
	keepEnd := 3
	if keepEnd > len(messages)-1 {
		keepEnd = len(messages) - 1
	}

	protected := make(map[int]bool)
	protected[0] = true
	for i := len(messages) - keepEnd; i < len(messages); i++ {
		protected[i] = true
	}
	for i, m := range messages {
		if m.Pinned {
			protected[i] = true
		}
	}

	// keepMask[i] = true means message i is currently kept.
	keepMask := make([]bool, len(messages))
	for i := range messages {
		keepMask[i] = true
	}

	// Pass 1: drop Prunable non-protected messages oldest-first.
	for i := range messages {
		if protected[i] || !messages[i].Prunable {
			continue
		}
		if currentTotalWithMask(messages, keepMask, est) <= budget {
			break
		}
		keepMask[i] = false
	}

	// Pass 2: if still over budget, drop any non-protected (including
	// non-Prunable user/assistant turns) oldest-first.
	for i := range messages {
		if protected[i] || !keepMask[i] {
			continue
		}
		if currentTotalWithMask(messages, keepMask, est) <= budget {
			break
		}
		keepMask[i] = false
	}

	// Assemble result in original order.
	result := make([]Message, 0, len(messages))
	for i, m := range messages {
		if keepMask[i] {
			result = append(result, m)
		}
	}

	// If even keeping only protected we're over budget, return only protected.
	if currentTotalWithMask(messages, keepMask, est) > budget {
		var minimal []Message
		for i, m := range messages {
			if protected[i] {
				minimal = append(minimal, m)
			}
		}
		return minimal
	}

	return result
}

// currentTotalWithMask sums tokens for messages whose keepMask entry is true.
func currentTotalWithMask(messages []Message, keepMask []bool, est TokenEstimator) int {
	total := 0
	for i, m := range messages {
		if keepMask[i] {
			total += est.Estimate(m.Content) + 4
		}
	}
	return total
}

func totalTokens(messages []Message, est TokenEstimator) int {
	total := 0
	for _, m := range messages {
		total += est.Estimate(m.Content) + 4 // ~4 tokens overhead per message
	}
	return total
}

