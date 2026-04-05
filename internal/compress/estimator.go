// Package compress provides tiered context compression for LLM conversations.
// Keeps conversations within context window limits by smart truncation,
// LLM-based summarization, and emergency recompression.
package compress

// TokenEstimator estimates token count from text.
type TokenEstimator interface {
	Estimate(text string) int
}

// SimpleEstimator uses ~4 characters per token for English text.
// Accurate enough for budget decisions without requiring a tokenizer.
type SimpleEstimator struct{}

// Estimate returns an approximate token count.
func (e *SimpleEstimator) Estimate(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4 // ceil division
}
