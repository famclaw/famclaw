package compress

import (
	"strings"
	"testing"
)

func TestSimpleEstimator(t *testing.T) {
	est := &SimpleEstimator{}

	tests := []struct {
		text string
		want int
	}{
		{"", 0},
		{"hi", 1},
		{"hello world", 3},       // 11 chars / 4 = 2.75 → ceil = 3
		{strings.Repeat("a", 100), 25}, // 100 / 4 = 25
		{strings.Repeat("a", 101), 26}, // 101 / 4 = 25.25 → ceil = 26
	}

	for _, tt := range tests {
		got := est.Estimate(tt.text)
		if got != tt.want {
			t.Errorf("Estimate(%d chars) = %d, want %d", len(tt.text), got, tt.want)
		}
	}
}

func makeMessages(count int, contentLen int) []Message {
	msgs := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
	}
	for i := 0; i < count; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, Message{
			Role:    role,
			Content: strings.Repeat("x", contentLen),
		})
	}
	return msgs
}

func TestCompressFitsAlready(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "short"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	result := Compress(msgs, Options{ContextWindow: 4096})
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no compression needed), got %d", len(result))
	}
}

func TestCompressDropsOldest(t *testing.T) {
	// 30 messages with 100 chars each ≈ 30 * (25 + 4) = 870 tokens
	// With a tiny window of 200 tokens, must drop many
	msgs := makeMessages(30, 100)
	opts := Options{ContextWindow: 300}

	result := Compress(msgs, opts)

	if len(result) >= len(msgs) {
		t.Errorf("expected compression, got %d messages (same as input %d)", len(result), len(msgs))
	}

	// System prompt should always be kept
	if result[0].Role != "system" {
		t.Error("system prompt should be first message")
	}

	// Last messages should be preserved
	lastOriginal := msgs[len(msgs)-1]
	lastResult := result[len(result)-1]
	if lastResult.Content != lastOriginal.Content {
		t.Error("last message should be preserved")
	}
}

func TestCompressPreservesPinned(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: strings.Repeat("x", 100)},
		{Role: "user", Content: strings.Repeat("a", 100)},
		{Role: "assistant", Content: strings.Repeat("b", 100), Pinned: true}, // pinned
		{Role: "user", Content: strings.Repeat("c", 100)},
		{Role: "assistant", Content: strings.Repeat("d", 100)},
		{Role: "user", Content: strings.Repeat("e", 100)},
		{Role: "assistant", Content: strings.Repeat("f", 100)},
	}

	// Tiny window forces compression
	result := Compress(msgs, Options{ContextWindow: 200})

	// Check pinned message is preserved
	found := false
	for _, m := range result {
		if m.Content == strings.Repeat("b", 100) {
			found = true
			break
		}
	}
	if !found {
		t.Error("pinned message should be preserved during compression")
	}
}

func TestCompressTooFewMessages(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: strings.Repeat("x", 1000)},
		{Role: "user", Content: "hi"},
	}

	// Even with tiny window, too few to truncate
	result := Compress(msgs, Options{ContextWindow: 50})
	if len(result) != 2 {
		t.Errorf("expected 2 messages (too few to truncate), got %d", len(result))
	}
}

func TestCompressDefaultWindow(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "hi"},
		{Role: "user", Content: "hello"},
	}

	// ContextWindow=0 should default to 4096
	result := Compress(msgs, Options{})
	if len(result) != 2 {
		t.Errorf("expected 2 messages with default window, got %d", len(result))
	}
}

func TestTotalTokens(t *testing.T) {
	est := &SimpleEstimator{}
	msgs := []Message{
		{Role: "system", Content: strings.Repeat("x", 40)},  // 10 + 4 = 14
		{Role: "user", Content: strings.Repeat("x", 80)},    // 20 + 4 = 24
	}
	total := totalTokens(msgs, est)
	if total != 38 { // 14 + 24
		t.Errorf("totalTokens = %d, want 38", total)
	}
}
