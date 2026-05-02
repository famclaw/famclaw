package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestChunkMessage was drafted by qwen3:14b (think:false), reviewed and
// finalized by hand. Cases trace cleanly:
//   - "split on newlines": 500×"line\n" (2500 bytes), maxLen=2000.
//     First iteration finds '\n' at byte 1999 (end of the 400th "line\n"),
//     chunk = first 2000 bytes, remaining = 500 bytes → 2 chunks total.
//   - "no newlines": 5000×"a", maxLen=2000 → 2000+2000+1000 = 3 chunks.
func TestChunkMessage(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		maxLen    int
		wantCount int
	}{
		{"short", "hello", 2000, 1},
		{"exact limit", strings.Repeat("a", 2000), 2000, 1},
		{"split on newlines", strings.Repeat("line\n", 500), 2000, 2},
		{"no newlines", strings.Repeat("a", 5000), 2000, 3},
		{"empty", "", 2000, 1},
		{"maxLen zero", "hello", 0, 1},
		{"maxLen negative", "hello", -5, 1},
		// Rune-safe split (CodeRabbit thread): 8 emoji × 4 bytes = 32 bytes,
		// maxLen=10 walks back to a rune boundary so each chunk holds 2 emoji
		// (8 bytes). Without rune-safety, this would split a 4-byte emoji
		// in half and produce invalid UTF-8.
		{"emoji rune-safe", strings.Repeat("\U0001F600", 8), 10, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkMessage(tt.text, tt.maxLen)
			if len(got) != tt.wantCount {
				t.Errorf("ChunkMessage(_, %d) got %d chunks, want %d", tt.maxLen, len(got), tt.wantCount)
			}
			if tt.maxLen > 0 {
				for i, chunk := range got {
					if len(chunk) > tt.maxLen {
						t.Errorf("chunk[%d] len=%d exceeds maxLen=%d", i, len(chunk), tt.maxLen)
					}
				}
			}
			if strings.Join(got, "") != tt.text {
				t.Errorf("joined chunks != original text (lengths %d vs %d)", len(strings.Join(got, "")), len(tt.text))
			}
			// CodeRabbit thread #2: each chunk must be valid UTF-8 when
			// the input is. We don't fabricate input that's already
			// invalid, so any chunk that fails utf8.ValidString points to
			// a mid-rune split.
			if utf8.ValidString(tt.text) {
				for i, chunk := range got {
					if !utf8.ValidString(chunk) {
						t.Errorf("chunk[%d] = %q is not valid UTF-8", i, chunk)
					}
				}
			}
		})
	}
}
