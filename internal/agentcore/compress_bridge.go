// compress_bridge.go: helpers to bridge llm.Message <-> compress.Message for in-pipeline compression.
package agentcore

import (
	"encoding/json"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

// llmToCompress converts an []llm.Message to []compress.Message, marking
// the indexes in prunableIdx as Prunable. Pinned is NOT set here —
// CompressIndices already protects index 0 (system prompt) and the most
// recent user-role message automatically.
//
// Tool-call identities are carried opaquely into CallIDs / ToolCallID so
// Compress can keep each assistant call / tool-result pair together.
// Serialized tool argument payloads are carried in ToolArgs so token
// estimation accounts for large arguments on the wire. No interpretation
// of tool names, arguments or content is performed.
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
			cm.ToolArgs = make([]string, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				cm.CallIDs[j] = tc.ID
				// One serialized payload per call, preserving order.
				// Parallel calls to the same tool name each get their
				// own entry — no keying by name.
				cm.ToolArgs[j] = toolCallArgumentsString(tc)
			}
		}
		out[i] = cm
	}
	return out
}

// compressToLLM restores the original llm.Messages at the positions
// selected by CompressIndices. Because the selection is authoritative
// (one position per retained message, in original order), no
// comparator-based matching is performed. Each original message is
// returned verbatim, preserving ToolCalls, ToolCallID, Reasoning,
// ContentParts and every other field.
func compressToLLM(orig []llm.Message, idx []int) []llm.Message {
	out := make([]llm.Message, 0, len(idx))
	for _, i := range idx {
		if i >= 0 && i < len(orig) {
			out = append(out, orig[i])
		}
	}
	return out
}

// toolCallArgumentsString returns the JSON-encoded string form of
// ToolCallFunction.Arguments, exactly as it appears on the wire.
// This is used for opaque token accounting in compress.Message.ToolArgs.
func toolCallArgumentsString(tc llm.ToolCall) string {
	data, err := json.Marshal(tc.Function.Arguments)
	if err != nil {
		return tc.Function.Name
	}
	return string(data)
}
