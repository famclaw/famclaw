// Package claudecli implements llm.Chatter by shelling out to the `claude` CLI.
// It spawns: claude -p "<prompt>" and returns stdout as the response.
// Streaming is not supported in v1; onToken is called once with the full response.
package claudecli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/famclaw/famclaw/internal/llm"
)

// Client implements llm.Chatter by shelling out to the `claude` CLI.
// It spawns: claude -p "<prompt>" and returns the stdout as the response.
// Streaming is not supported in v1; onToken is called once with the full response.
type Client struct{}

// New returns a new Client. The client has no configuration — it uses
// whatever `claude` binary is found on $PATH at call time.
func New() *Client {
	return &Client{}
}

// compile-time assertion that *Client implements llm.Chatter
var _ llm.Chatter = (*Client)(nil)

// lastUserText returns the text of the last user message in the slice.
// Returns an empty string if no user message is found.
func lastUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// Chat sends the last user message to the claude CLI and returns the full response.
// onToken is called once with the full response text (no streaming in v1).
// temp and maxTokens are ignored — the claude CLI does not expose these flags in v1.
func (c *Client) Chat(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	userText := lastUserText(messages)

	out, err := exec.CommandContext(ctx, "claude", "-p", userText).Output()
	if err != nil {
		return "", fmt.Errorf("claude cli: %w", err)
	}

	response := strings.TrimRight(string(out), "\n")

	if onToken != nil {
		onToken(response)
	}

	return response, nil
}

// ChatMessage sends the last user message to the claude CLI and returns the
// response as a *llm.Message with Role "assistant".
// temp and maxTokens are ignored — the claude CLI does not expose these flags in v1.
func (c *Client) ChatMessage(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (*llm.Message, error) {
	text, err := c.Chat(ctx, messages, temp, maxTokens, nil)
	if err != nil {
		return nil, err
	}
	return &llm.Message{Role: "assistant", Content: text}, nil
}

// ChatWithTools is not supported in v1 of the claude CLI adapter.
// Tool calls require structured JSON output that the CLI does not provide.
func (c *Client) ChatWithTools(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, tools []llm.ToolDef) (*llm.Message, error) {
	return nil, fmt.Errorf("claude cli: tool calls not supported in v1")
}

// ChatSync sends the last user message to the claude CLI and returns the full
// response as a plain string (non-streaming).
// temp and maxTokens are ignored — the claude CLI does not expose these flags in v1.
func (c *Client) ChatSync(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (string, error) {
	return c.Chat(ctx, messages, temp, maxTokens, nil)
}
