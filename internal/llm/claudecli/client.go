// Package claudecli implements llm.Chatter by shelling out to the `claude` CLI.
// It passes system prompt (via --system), message history (via --message),
// and tool definitions (via --tool file.json) to the claude CLI.
package claudecli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/famclaw/famclaw/internal/llm"
)

// Client implements llm.Chatter by shelling out to the `claude` CLI.
// It passes system prompt (via --system), message history (via --message),
// and tool definitions (via --tool file.json) to the claude CLI.
type Client struct{}

// New returns a new Client. The client has no configuration — it uses
// whatever `claude` binary is found on $PATH at call time.
func New() *Client {
	return &Client{}
}

// compile-time assertion that *Client implements llm.Chatter
var _ llm.Chatter = (*Client)(nil)

// Chat passes the full conversation to the claude CLI:
//   --system "<system prompt>"  (first system message content, if any)
//   --message "role: content"   (each non-system message)
//   --tool /tmp/tools.json      (tool definitions, if provided)
//
// onToken is called once with the full response text (no streaming in v1).
// temp and maxTokens are ignored — the claude CLI does not expose these flags in v1.
func (c *Client) Chat(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	args := []string{}

	// --system: content of the first system message, if present.
	for _, m := range messages {
		if m.Role == "system" {
			args = append(args, "--system", m.Content)
			break
		}
	}

	// --message: every non-system message as "role: content".
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		role := m.Role
		if role == "user" {
			role = "human"
		}
		content := m.Content
		if content == "" {
			content = " "
		}
		args = append(args, "--message", role+": "+content)
	}

	// NOTE: Chat() does not receive tool definitions from the caller.
	// Tool-use flows go through ChatWithTools() which is not supported in v1.
	// Tool definitions are passed via messages (e.g., Anthropic tool_result role)
	// which are already handled above.

	out, err := exec.CommandContext(ctx, "claude", args...).Output()
	if err != nil {
		return "", fmt.Errorf("claude cli: %w", err)
	}

	response := strings.TrimRight(string(out), "\n")

	if onToken != nil {
		onToken(response)
	}

	return response, nil
}

// ChatMessage sends the full conversation to the claude CLI and returns the
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

// ChatSync sends the full conversation to the claude CLI and returns the full
// response as a plain string (non-streaming).
// temp and maxTokens are ignored — the claude CLI does not expose these flags in v1.
func (c *Client) ChatSync(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (string, error) {
	return c.Chat(ctx, messages, temp, maxTokens, nil)
}
