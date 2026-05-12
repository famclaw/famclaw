// compress_bridge.go: helpers to bridge llm.Message <-> compress.Message for in-pipeline compression.
package agentcore

import (
	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

// llmToCompress converts an []llm.Message to []compress.Message, marking
// the indexes in prunableIdx as Prunable. Pinned is NOT set here — Compress
// already protects index 0 (system prompt) automatically.
func llmToCompress(in []llm.Message, prunableIdx map[int]bool) []compress.Message {
	out := make([]compress.Message, len(in))
	for i, msg := range in {
		out[i] = compress.Message{
			Role:     msg.Role,
			Content:  msg.Content,
			Prunable: prunableIdx[i],
		}
	}
	return out
}

// compressToLLM walks orig and c in lockstep, returning the subset of orig
// whose (Role, Content) matches the next c entry. Preserves ToolCalls and
// ToolCallID from the original llm.Message. Handles the drop-only case;
// inserted messages (e.g. synthetic summaries from Tier 1) that don't
// match any orig entry are carried as plain llm.Message{Role, Content}
// with no tool data.
func compressToLLM(c []compress.Message, orig []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(c))
	j := 0
	for i := 0; i < len(c); i++ {
		for j < len(orig) && (orig[j].Role != c[i].Role || orig[j].Content != c[i].Content) {
			j++
		}
		if j < len(orig) && orig[j].Role == c[i].Role && orig[j].Content == c[i].Content {
			out = append(out, orig[j])
			j++
		} else {
			out = append(out, llm.Message{
				Role:    c[i].Role,
				Content: c[i].Content,
			})
		}
	}
	return out
}
