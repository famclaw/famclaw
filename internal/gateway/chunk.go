package gateway

import (
	"strings"
	"unicode/utf8"
)

// ChunkMessage splits text into chunks, each at most maxLen bytes long.
// When maxLen <= 0 or len(text) <= maxLen, the input is returned unchanged
// as a single-element slice.
//
// Chunk-break preference (best to worst):
//  1. End of a triple-backtick fenced code block (don't tear a code block)
//  2. Paragraph boundary — a blank line (\n\n)
//  3. Single newline
//  4. Sentence ending — `. `, `? `, or `! ` followed by a space
//  5. Word boundary — a space
//  6. UTF-8 rune boundary (so multi-byte runes don't get bisected)
//
// Each rule is consulted only within the first maxLen bytes of what's
// left to chunk. If no boundary at a given level exists in the window,
// fall through to the next level. The terminal level (rune boundary) is
// always reachable, so this function always terminates.
//
// Every returned element has length <= maxLen when maxLen > 0.
func ChunkMessage(text string, maxLen int) []string {
	if maxLen <= 0 {
		return []string{text}
	}
	if len(text) <= maxLen {
		return []string{text}
	}

	var result []string
	remaining := text
	for len(remaining) > maxLen {
		window := remaining[:maxLen]

		// 1. Try to break at the end of a fenced code block. We scan for
		//    the LAST closing ``` in the window — if found, that's the
		//    cleanest split point because it doesn't tear a code block.
		if cut := lastCodeFenceEnd(window); cut > 0 {
			result = append(result, remaining[:cut])
			remaining = remaining[cut:]
			continue
		}

		// 2. Paragraph break — last "\n\n" in window. The blank line
		//    stays with the preceding chunk.
		if idx := strings.LastIndex(window, "\n\n"); idx != -1 {
			cut := idx + 2
			result = append(result, remaining[:cut])
			remaining = remaining[cut:]
			continue
		}

		// 3. Single newline.
		if idx := strings.LastIndex(window, "\n"); idx != -1 {
			result = append(result, remaining[:idx+1])
			remaining = remaining[idx+1:]
			continue
		}

		// 4. Sentence ending. Pick the last `. `, `? `, or `! ` in window.
		if cut := lastSentenceEnd(window); cut > 0 {
			result = append(result, remaining[:cut])
			remaining = remaining[cut:]
			continue
		}

		// 5. Word boundary — last space.
		if idx := strings.LastIndex(window, " "); idx > 0 {
			result = append(result, remaining[:idx+1])
			remaining = remaining[idx+1:]
			continue
		}

		// 6. UTF-8 rune boundary fallback.
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(remaining[cut]) {
			cut--
		}
		if cut == 0 {
			// Pathological: the entire window is mid-rune (only possible
			// for invalid UTF-8 or maxLen < 4). Fall back to a byte split
			// rather than infinite-looping; one chunk will have a broken
			// rune, but the platform will render a replacement glyph.
			cut = maxLen
		}
		result = append(result, remaining[:cut])
		remaining = remaining[cut:]
	}
	result = append(result, remaining)
	return result
}

// lastCodeFenceEnd returns a position just after the last closing ```
// in the window, or 0 if no closing fence is present. Only emits a cut
// when the fence count is even (opens and closes are balanced) — an odd
// count means we'd be cutting INSIDE an open code block, which is worse
// than the next-tier boundaries.
func lastCodeFenceEnd(window string) int {
	count := strings.Count(window, "```")
	if count == 0 || count%2 != 0 {
		return 0
	}
	idx := strings.LastIndex(window, "```")
	if idx < 0 {
		return 0
	}
	cut := idx + 3
	// Include the trailing newline if present, so the next chunk starts
	// at the beginning of a line.
	if cut < len(window) && window[cut] == '\n' {
		cut++
	}
	return cut
}

// lastSentenceEnd returns the byte position just after the last sentence
// terminator (`. ` / `? ` / `! ` / `.` at end-of-string), or 0 if none.
func lastSentenceEnd(window string) int {
	best := -1
	for _, sep := range []string{". ", "? ", "! "} {
		if idx := strings.LastIndex(window, sep); idx > best {
			best = idx + len(sep)
		}
	}
	// Also accept a sentence terminator at the very end of the window
	// (no trailing space because the rest of the message follows on the
	// next chunk).
	for _, c := range []byte{'.', '?', '!'} {
		if last := strings.LastIndexByte(window, c); last > best && last == len(window)-1 {
			best = last + 1
		}
	}
	if best <= 0 {
		return 0
	}
	return best
}
