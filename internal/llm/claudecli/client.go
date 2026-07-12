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

// claudeInputEnvelope wraps the full conversation as a single NDJSON event.
// The claude CLI's --input-format stream-json expects user events of this
// form; it does not accept assistant events on stdin.
type claudeInputEnvelope struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string        `json:"role"`
		Content []llm.Message `json:"content"`
	} `json:"message"`
}

// claudeOutputEnvelope represents a line from `claude --output-format stream-json`.
// The CLI emits wrapped assistant message envelopes, not raw deltas.
type claudeOutputEnvelope struct {
	Type    string `json:"type"`
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason,omitempty"`
	} `json:"message,omitempty"`
}

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

// Chat passes the full conversation to the claude CLI using stream-json I/O:
//
//	--system-prompt "<system prompt>"  (first system message content, if any)
//	stdin: NDJSON — wrapped user events {"type":"user","message":{...}}
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

	// Collect non-system messages as a single NDJSON event.
	// The claude CLI's --input-format stream-json expects user events on stdin.
	// It does not accept assistant events — all conversation history is grouped
	// into one {"type":"user","message":{"content":[...]}} envelope.
	// The system message is excluded to avoid duplication with --system-prompt.
	var history []llm.Message
	for _, m := range messages {
		if m.Role != "system" {
			history = append(history, m)
		}
	}
	env, err := json.Marshal(claudeInputEnvelope{
		Type: "user",
		Message: &struct {
			Role    string        `json:"role"`
			Content []llm.Message `json:"content"`
		}{
			Role:    "user",
			Content: history,
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshaling claude stdin event: %w", err)
	}
	var ndjson bytes.Buffer
	ndjson.Write(env)
	ndjson.WriteByte('\n')

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

	// Write NDJSON messages to stdin from a goroutine to avoid deadlock.
	// If NDJSON exceeds the OS pipe buffer (~64KB) and the claude process
	// starts emitting stdout before draining stdin, both sides would block.
	errCh := make(chan error, 1)
	go func() {
		defer stdin.Close()
		_, err := stdin.Write(ndjson.Bytes())
		errCh <- err
	}()

	// Read and parse streaming output.
	var fullResponse strings.Builder
	scanner := bufio.NewScanner(stdout)
	// Increase max token size to handle large stream-json lines.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse the wrapped output envelope.
		var env claudeOutputEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			// Skip lines that don't parse as JSON.
			continue
		}

		// Extract text from assistant message content blocks.
		if env.Message != nil {
			for _, block := range env.Message.Content {
				if block.Type == "text" && block.Text != "" {
					if onToken != nil {
						onToken(block.Text)
					}
					fullResponse.WriteString(block.Text)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		cmd.Wait()
		return "", fmt.Errorf("reading claude output: %w", err)
	}

	// Wait for CLI to finish first.
	waitErr := cmd.Wait()

	// Wait for stdin writer to finish. If the process already exited
	// successfully (waitErr is nil), a broken-pipe write is benign and
	// must not discard the response we already collected.
	writeErr := <-errCh
	if waitErr != nil {
		return "", fmt.Errorf("claude cli: %w (%s)", waitErr, stderrBuf.String())
	}
	if writeErr != nil {
		// Process exited before reading all stdin (e.g., large history).
		// The response is already complete — surface the warning but
		// still return the collected text.
		return fullResponse.String(), fmt.Errorf("writing messages to stdin after cli exit: %w", writeErr)
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
