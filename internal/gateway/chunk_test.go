package gateway

import (
	"strings"
	"testing"
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
		})
	}
}
