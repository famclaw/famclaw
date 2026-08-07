// Package compress provides tiered context compression for LLM conversations.
// Keeps conversations within context window limits by smart truncation,
// LLM-based summarization, and emergency recompression.
package compress

// CharsPerToken is the heuristic for estimating tokens from text length.
// Exported so that budget calculations in other packages (e.g. agent) stay
// in sync with this estimator rather than duplicating the value as a magic
// number that can silently diverge.
const CharsPerToken = 4

// TokenEstimator estimates token count from text.
type TokenEstimator interface {
	Estimate(text string) int
}

// SimpleEstimator uses CharsPerToken for English text.
// Accurate enough for budget decisions without requiring a tokenizer.
type SimpleEstimator struct{}

// Estimate returns an approximate token count.
func (e *SimpleEstimator) Estimate(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + CharsPerToken - 1) / CharsPerToken // ceil division
}
