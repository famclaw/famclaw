// compress_bridge.go: helpers to bridge llm.Message <-> compress.Message for in-pipeline compression.
package agentcore

import (
	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

// llmToCompress converts an []llm.Message to []compress.Message, marking
// the indexes in prunableIdx as Prunable. Pinned is NOT set here — Compress
// already protects index 0 (system prompt) automatically.
//
// Tool-call identities are carried opaquely into CallIDs / ToolCallID so
// Compress can keep each assistant call / tool-result pair together. No
// interpretation of tool names, arguments or content is performed.
func llmToCompress(in []llm.Message, prunableIdx map[int]bool) []compress.Message {
	out := make([]compress.Message, len(in))
	for i, msg := range in {
		cm := compress.Message{
			Role:         msg.Role,
			Content:      msg.Content,
			ContentParts: msg.ContentParts,
			Prunable:     prunableIdx[i],
			ToolCallID:   msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			cm.CallIDs = make([]string, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				cm.CallIDs[j] = tc.ID
			}
		}
		out[i] = cm
	}
	return out
}

// compressToLLM maps the compressed slice back to original llm.Messages.
// Because Compress (Tier 0) is drop-only, the compressed list is a
// subsequence of cmsgs, which is itself 1:1 with orig. So we can walk
// cmsgs and orig in lockstep and pick the entries whose role/content
// match the next compressed entry — this restores ToolCalls, ToolCallID,
// Reasoning and every other original field verbatim.
//
// Synthetic inserted entries (future Tier 1) have no match in cmsgs and
// are emitted as plain {Role, Content} messages with no tool data.
func compressToLLM(cmsgs []compress.Message, compressed []compress.Message, orig []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(compressed))
	j := 0
	for i := 0; i < len(compressed); i++ {
		for j < len(cmsgs) && !sameCompress(cmsgs[j], compressed[i]) {
			j++
		}
		if j < len(cmsgs) && sameCompress(cmsgs[j], compressed[i]) && j < len(orig) {
			out = append(out, orig[j])
			j++
			continue
		}
		out = append(out, llm.Message{
			Role:    compressed[i].Role,
			Content: compressed[i].Content,
		})
	}
	return out
}

// sameCompress compares two compress.Message entries including the
// tool-call identity fields (CallIDs, ToolCallID). This prevents
// duplicate-content messages from being mis-matched during the lockstep
// walk.
func sameCompress(a, b compress.Message) bool {
	if a.Role != b.Role || a.Content != b.Content || a.ToolCallID != b.ToolCallID {
		return false
	}
	if len(a.CallIDs) != len(b.CallIDs) {
		return false
	}
	for i := range a.CallIDs {
		if a.CallIDs[i] != b.CallIDs[i] {
			return false
		}
	}
	return true
}
