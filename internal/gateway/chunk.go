package gateway

import "strings"

// ChunkMessage splits text into chunks, each at most maxLen bytes long.
// When maxLen <= 0 or len(text) <= maxLen, the input is returned unchanged
// as a single-element slice. Otherwise the function prefers to split at the
// last '\n' within the first maxLen bytes (the newline stays with the
// preceding chunk); if there's no newline in that window, it hard-splits
// at exactly maxLen bytes. Every returned element has length <= maxLen
// when maxLen > 0.
//
// Drafted by qwen3:14b (think:false), reviewed and finalized by hand.
//
// Known limitation flagged by mcp__ollama__local_review:
// the hard-split path (no newline in window) splits on byte boundaries.
// If the byte at index maxLen-1 is the middle of a multi-byte UTF-8
// rune (e.g., a 4-byte emoji), the chunk and the next chunk will each
// contain an invalid UTF-8 sequence. Telegram/Discord render these as
// replacement glyphs but do not error. Acceptable for v1; a rune-aware
// version would need to walk back from maxLen using utf8.RuneStart to
// find the last valid boundary.
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
		// Last '\n' in the first maxLen bytes — newline stays with the chunk.
		idx := strings.LastIndex(remaining[:maxLen], "\n")
		if idx != -1 {
			result = append(result, remaining[:idx+1])
			remaining = remaining[idx+1:]
		} else {
			result = append(result, remaining[:maxLen])
			remaining = remaining[maxLen:]
		}
	}
	result = append(result, remaining)
	return result
}
