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

// TestChunkMessage_PreferParagraphBreak asserts the new smart-boundary
// behavior: when a paragraph break (blank line) sits inside the window,
// the chunk cuts there even though earlier single newlines also exist.
func TestChunkMessage_PreferParagraphBreak(t *testing.T) {
	// 80-byte sentences. Three sentences, separated by single \n then
	// a paragraph break (\n\n), then more content.
	in := strings.Repeat("a", 80) + "\n" +
		strings.Repeat("b", 80) + "\n\n" +
		strings.Repeat("c", 80) + "\n" +
		strings.Repeat("d", 80)
	got := ChunkMessage(in, 200)
	// First chunk should end at the paragraph break (the \n\n at byte ~163),
	// not at one of the single newlines.
	if !strings.HasSuffix(got[0], "\n\n") {
		t.Errorf("first chunk should end at paragraph break (\\n\\n), got tail %q",
			tailN(got[0], 6))
	}
}

// TestChunkMessage_PreservesCodeBlockThatFits asserts that when a fenced
// code block IS smaller than maxLen, the chunker keeps it intact rather
// than splitting at an earlier newline inside the block.
//
// (A code block LARGER than maxLen is unavoidably torn — that case is
// out of scope for this test.)
func TestChunkMessage_PreservesCodeBlockThatFits(t *testing.T) {
	// 200-byte prose, then a 200-byte code block, then 100-byte prose.
	// maxLen=600 — entire code block fits in the first window.
	pre := strings.Repeat("p", 200) + "\n"
	codeBody := strings.Repeat("c", 190)
	code := "```\n" + codeBody + "\n```\n"
	post := strings.Repeat("q", 100)
	in := pre + code + post

	got := ChunkMessage(in, 600)
	if len(got) != 1 && len(got) != 2 {
		t.Fatalf("expected 1 or 2 chunks for %d-byte input with maxLen 600, got %d", len(in), len(got))
	}
	// Whichever chunk contains the opening fence must also contain the
	// closing fence (balanced).
	for i, c := range got {
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk[%d] has unbalanced ``` (count=%d): tail=%q",
				i, strings.Count(c, "```"), tailN(c, 30))
		}
	}
}

// TestChunkMessage_SentenceBoundary asserts the sentence-boundary rule
// fires when no newline / paragraph break is available in the window.
func TestChunkMessage_SentenceBoundary(t *testing.T) {
	in := strings.Repeat("Hello world. ", 50) // ~650 bytes, no newlines
	got := ChunkMessage(in, 100)
	// Each non-final chunk should end with ". " (sentence boundary).
	for i, c := range got[:len(got)-1] {
		if !strings.HasSuffix(c, ". ") {
			t.Errorf("chunk[%d] should end at sentence boundary, got tail %q",
				i, tailN(c, 6))
		}
	}
}

func tailN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
