package compress

// Message mirrors the conversation message format.
type Message struct {
	Role    string
	Content string
	Pinned  bool // pinned messages survive truncation
}

// Options controls compression behavior.
type Options struct {
	ContextWindow int            // total context window in tokens
	Estimator     TokenEstimator // token estimator (defaults to SimpleEstimator)
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

	if total <= budget {
		return messages // fits already
	}

	return truncate(messages, budget, est)
}

// truncate implements Tier 0: drop oldest non-protected messages.
// Protected: system prompt (index 0), pinned messages, last 3 messages.
func truncate(messages []Message, budget int, est TokenEstimator) []Message {
	if len(messages) <= 4 {
		return messages // too few to truncate
	}

	// Always keep: system prompt (first), last 3 messages
	keepEnd := 3 // last 3 messages always kept
	if keepEnd > len(messages)-1 {
		keepEnd = len(messages) - 1
	}

	// Collect protected indices
	protected := make(map[int]bool)
	protected[0] = true // system prompt
	for i := len(messages) - keepEnd; i < len(messages); i++ {
		protected[i] = true
	}
	for i, m := range messages {
		if m.Pinned {
			protected[i] = true
		}
	}

	// Start from oldest non-protected and drop until within budget
	result := make([]Message, 0, len(messages))
	dropped := 0

	for i, m := range messages {
		if protected[i] {
			result = append(result, m)
			continue
		}
		// Check if we still need to drop
		currentTotal := totalTokens(messages, est) - droppedTokens(messages[:i+1], protected, est, dropped)
		if currentTotal > budget {
			dropped++
			continue // drop this message
		}
		result = append(result, m)
	}

	// If still over budget after dropping all droppable, just keep protected
	if totalTokens(result, est) > budget {
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

func totalTokens(messages []Message, est TokenEstimator) int {
	total := 0
	for _, m := range messages {
		total += est.Estimate(m.Content) + 4 // ~4 tokens overhead per message
	}
	return total
}

func droppedTokens(messages []Message, protected map[int]bool, est TokenEstimator, maxDrop int) int {
	total := 0
	count := 0
	for i, m := range messages {
		if !protected[i] && count < maxDrop {
			total += est.Estimate(m.Content) + 4
			count++
		}
	}
	return total
}
