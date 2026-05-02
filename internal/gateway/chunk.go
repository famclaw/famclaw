package gateway

import (
	"strings"
	"unicode/utf8"
)

// ChunkMessage splits text into chunks, each at most maxLen bytes long.
// When maxLen <= 0 or len(text) <= maxLen, the input is returned unchanged
// as a single-element slice. Otherwise the function prefers to split at the
// last '\n' within the first maxLen bytes (the newline stays with the
// preceding chunk); if there's no newline in that window, it walks back
// from maxLen to the most recent UTF-8 rune boundary so that hard-splits
// don't bisect a multi-byte rune. Every returned element has length
// <= maxLen when maxLen > 0.
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
		// Prefer last '\n' in the first maxLen bytes — newline stays with
		// the chunk and the next chunk starts cleanly.
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx != -1 {
			result = append(result, remaining[:idx+1])
			remaining = remaining[idx+1:]
			continue
		}

		// No newline. Walk back from maxLen to the last position where the
		// next chunk can start on a UTF-8 rune boundary — i.e., where
		// remaining[cut] is the first byte of a rune (not a continuation byte).
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
