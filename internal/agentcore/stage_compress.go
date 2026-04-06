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

		// Convert to compress.Message format
		msgs := make([]compress.Message, len(turn.Messages))
		for i, m := range turn.Messages {
			msgs[i] = compress.Message{
				Role:    m.Role,
				Content: m.Content,
				Pinned:  m.Role == "system", // system prompt always pinned
			}
		}

		// Compress
		compressed := compress.Compress(msgs, compress.Options{
			ContextWindow: contextWindow,
		})

		// Convert back
		turn.Messages = make([]Message, len(compressed))
		for i, m := range compressed {
			turn.Messages[i] = Message{
				Role:    m.Role,
				Content: m.Content,
			}
		}

		return nil
	}
}
