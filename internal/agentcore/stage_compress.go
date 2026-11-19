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

		// Compress
		compressed := compress.Compress(msgs, compress.Options{
			ContextWindow: contextWindow,
		})

		// Convert back. Because Compress is drop-only, each compressed entry
		// maps to exactly one original turn.Message. Use identity-aware
		// matching (Role+Content+CallIDs+ToolCallID) to avoid aliasing
		// duplicate-content messages.
		out := make([]Message, len(compressed))
		j := 0
		for i, m := range compressed {
			for j < len(msgs) && !sameCompressStage(msgs[j], m) {
				j++
			}
			if j < len(msgs) && sameCompressStage(msgs[j], m) {
				orig := turn.Messages[j]
				out[i] = Message{
					Role:         m.Role,
					Content:      m.Content,
					ContentParts: m.ContentParts,
					ToolCalls:    orig.ToolCalls,
				}
				j++
				continue
			}
			out[i] = Message{
				Role:         m.Role,
				Content:      m.Content,
				ContentParts: m.ContentParts,
			}
		}
		turn.Messages = out

		return nil
	}
}

// sameCompressStage compares two compress.Message entries including the
// tool-call identity fields (CallIDs, ToolCallID). This prevents
// duplicate-content messages from being mis-matched during the lockstep
// walk.
func sameCompressStage(a, b compress.Message) bool {
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
