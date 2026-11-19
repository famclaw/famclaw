// compress_bridge.go: helpers to bridge llm.Message <-> compress.Message for in-pipeline compression.
package agentcore

import (
	"encoding/json"

	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/llm"
)

// LLMToCompress converts an []llm.Message to []compress.Message, marking
// the indexes in prunableIdx as Prunable. Pinned is NOT set here —
// CompressIndices already protects index 0 (system prompt) and the most
// recent user-role message automatically.
//
// Tool-call identities are carried opaquely into CallIDs / ToolCallID so
// Compress can keep each assistant call / tool-result pair together.
// Serialized tool argument payloads are carried in ToolArgs so token
// estimation accounts for large arguments on the wire. No interpretation
// of tool names, arguments or content is performed.
//
// This is the single opaque LLM→compress bridge shared by the pipeline
// (StageCompress, tool-loop) and by Agent.buildMessages, so the direct
// build path does not maintain a separate lossy compressor.
func LLMToCompress(in []llm.Message, prunableIdx map[int]bool) []compress.Message {
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
				cm.ToolArgs[j] = toolCallFunctionString(tc)
			}
		}
		out[i] = cm
	}
	return out
}

// CompressToLLM restores the original llm.Messages at the positions
// selected by CompressIndices. Because the selection is authoritative
// (one position per retained message, in original order), no
// comparator-based matching is performed. Each original message is
// returned verbatim, preserving ToolCalls, ToolCallID, Reasoning,
// ContentParts and every other field.
func CompressToLLM(orig []llm.Message, idx []int) []llm.Message {
	out := make([]llm.Message, 0, len(idx))
	for _, i := range idx {
		if i >= 0 && i < len(orig) {
			out = append(out, orig[i])
		}
	}
	return out
}

// toolCallFunctionString returns the complete serialized function payload
// (name + arguments) for a single tool call, exactly as it appears on the
// wire. This is used for opaque token accounting in compress.Message.
// No parsing or interpretation of names/arguments is performed.
func toolCallFunctionString(tc llm.ToolCall) string {
	data, err := json.Marshal(tc.Function)
	if err != nil {
		// Fallback: at minimum count the name.
		return tc.Function.Name
	}
	return string(data)
}

// LLMToCore converts []llm.Message to []agentcore-style Message, copying
// every field the core message type supports: Role, Content,
// ContentParts, the complete ToolCalls slice (ID / Type / Function.Name /
// Function.Arguments) and ToolCallID. This is the single complete typed
// llm→core conversion; callers that need the full outgoing message list
// (e.g. Agent.buildMessages → Chat) use it instead of a hand-rolled
// field-by-field copy that would silently drop fields.
//
// Opaque tool-call data is copied verbatim without interpreting names,
// arguments or provider semantics. Normal DB history is plain
// role+content (no tool-call metadata), so this is a no-op beyond
// Role/Content/ContentParts for that common case; it becomes complete
// when a source actually carries tool calls.
func LLMToCore(in []llm.Message) []Message {
	out := make([]Message, len(in))
	for i, m := range in {
		out[i] = Message{
			Role:         m.Role,
			Content:      m.Content,
			ContentParts: m.ContentParts,
			ToolCallID:   m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			out[i].ToolCalls = make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				out[i].ToolCalls[j] = ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: copyToolArguments(tc.Function.Arguments),
					},
				}
			}
		}
	}
	return out
}

// copyToolArguments returns a copy of the tool-call arguments map so the
// core message owns its data. An empty/nil argument payload yields a nil
// map (JSON-omitted, matching the source's absence of arguments).
func copyToolArguments(args llm.ToolCallArguments) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

// CoreToLLM converts []Message back to []llm.Message, the inverse of
// LLMToCore, copying Role, Content, ContentParts, the complete ToolCalls
// slice and ToolCallID. It is the projection used to verify round-trips
// (LLMToCore → CoreToLLM restores the original outgoing message list).
func CoreToLLM(in []Message) []llm.Message {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		out[i] = llm.Message{
			Role:         m.Role,
			Content:      m.Content,
			ContentParts: m.ContentParts,
			ToolCallID:   m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			out[i].ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				var args llm.ToolCallArguments
				if len(tc.Function.Arguments) > 0 {
					args = make(llm.ToolCallArguments, len(tc.Function.Arguments))
					for k, v := range tc.Function.Arguments {
						args[k] = v
					}
				}
				out[i].ToolCalls[j] = llm.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: llm.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: args,
					},
				}
			}
		}
	}
	return out
}
