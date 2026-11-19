package agentcore

import (
	"context"

	"github.com/famclaw/famclaw/internal/compress"
)

// NewStageCompress returns a stage that compresses the conversation history
// to fit within the context window. Runs before the LLM call.
func NewStageCompress(contextWindow int) Stage {
	return func(_ context.Context, turn *Turn) error {
		if len(turn.Messages) <= 4 {
			return nil // too few to compress
		}

		// Convert to compress.Message format. Tool results are marked
		// Prunable so they evict before conversation turns; tool-call
		// identities are carried opaquely so groups stay intact.
		msgs := make([]compress.Message, len(turn.Messages))
		for i, m := range turn.Messages {
			cm := compress.Message{
				Role:         m.Role,
				Content:      m.Content,
				ContentParts: m.ContentParts,
				Pinned:       m.Role == "system", // system prompt always pinned
				Prunable:     m.Role == "tool",
			}
			if len(m.ToolCalls) > 0 {
				cm.CallIDs = make([]string, len(m.ToolCalls))
				for k, tc := range m.ToolCalls {
					cm.CallIDs[k] = tc.ID
				}
			}
			msgs[i] = cm
		}

		// Single authoritative selection: CompressIndices returns the
		// original positions of retained messages. Project the original
		// agentcore.Messages from those positions — no comparator matching.
		opts := compress.Options{ContextWindow: contextWindow}
		idx := compress.CompressIndices(msgs, opts)

		out := make([]Message, 0, len(idx))
		for _, i := range idx {
			if i >= 0 && i < len(turn.Messages) {
				out = append(out, turn.Messages[i])
			}
		}
		turn.Messages = out

		return nil
	}
}
