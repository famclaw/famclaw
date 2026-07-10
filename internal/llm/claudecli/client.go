// Package claudecli implements llm.Chatter by shelling out to the `claude` CLI.
// It passes system prompt (via --system-prompt), message history (via --input-format stream-json
// on stdin), and supports streaming output (via --output-format stream-json) to the claude CLI.
package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/famclaw/famclaw/internal/llm"
)

// Client implements llm.Chatter by shelling out to the `claude` CLI.
// It passes system prompt (via --system-prompt), message history (via --input-format stream-json
// on stdin), and supports streaming output (via --output-format stream-json).
type Client struct{}

// New returns a new Client. The client has no configuration — it uses
// whatever `claude` binary is found on $PATH at call time.
func New() *Client {
	return &Client{}
}

// compile-time assertion that *Client implements llm.Chatter
var _ llm.Chatter = (*Client)(nil)

// claudeStreamChunk represents a single chunk from the claude CLI's --output-format stream-json.
type claudeStreamChunk struct {
	Type         string `json:"type"`
	ContentBlock *struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Index int    `json:"index"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	Index      int    `json:"index,omitempty"`
}

// Chat passes the full conversation to the claude CLI using stream-json I/O:
//
//	--system-prompt "<system prompt>"  (first system message content, if any)
//	stdin: JSON array of messages (all messages including system)
//	--output-format stream-json        (realtime token streaming)
//
// onToken is called for each content chunk as it arrives.
// temp and maxTokens are not passed — the claude CLI uses its defaults.
func (c *Client) Chat(ctx context.Context, messages []llm.Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	if len(messages) == 0 {
		if onToken != nil {
			onToken("")
		}
		return "", nil
	}

	// Extract system prompt from the first system message.
	var systemPrompt string
	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			break
		}
	}

	// Marshal all messages as JSON for stdin.
	msgJSON, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("marshaling messages for claude: %w", err)
	}

	// Build CLI arguments.
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}

	// Start the claude process with stdin pipe.
	cmd := exec.CommandContext(ctx, "claude", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return "", fmt.Errorf("creating stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return "", fmt.Errorf("starting claude: %w", err)
	}

	// Write messages to stdin and close.
	if _, err := stdin.Write(msgJSON); err != nil {
		stdin.Close()
		cmd.Wait()
		return "", fmt.Errorf("writing messages to stdin: %w", err)
	}
	stdin.Close()

	// Read and parse streaming output.
	var fullResponse strings.Builder
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk claudeStreamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			// Skip lines that don't parse as JSON.
			continue
		}

		// Extract text from content block deltas.
		if chunk.Delta != nil {
			if chunk.Delta.Text != "" {
				if onToken != nil {
					onToken(chunk.Delta.Text)
				}
				fullResponse.WriteString(chunk.Delta.Text)
			}
		} else if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "text" {
			if chunk.ContentBlock.Text != "" {
				if onToken != nil {
					onToken(chunk.ContentBlock.Text)
				}
				fullResponse.WriteString(chunk.ContentBlock.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		cmd.Wait()
		return "", fmt.Errorf("reading claude output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("claude cli: %w (%s)", err, stderrBuf.String())
	}

	return fullResponse.String(), nil
}

// ChatMessage sends the full conversation to the claude CLI and returns the
// response as a *llm.Message with Role "assistant".
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
func (c *Client) ChatSync(ctx context.Context, messages []llm.Message, temp float64, maxTokens int) (string, error) {
	return c.Chat(ctx, messages, temp, maxTokens, nil)
}
