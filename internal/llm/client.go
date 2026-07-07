// Package llm provides a client for LLM inference via any OpenAI-compatible API.
// Works with: Ollama (v0.1.24+), llama.cpp server, Groq, OpenAI, OpenRouter.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrToolCallArgsTruncated is returned when an OpenAI-spec string-encoded
// tool-call arguments payload cannot be parsed because the inner JSON is
// incomplete. Local models occasionally truncate mid-emit under load.
// Callers can use errors.Is to surface a clean retry message instead of
// failing the whole turn with a low-level parser error.
var ErrToolCallArgsTruncated = errors.New("tool call arguments JSON truncated")

// Message is a conversation turn.
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // present when LLM requests tool use
	ToolCallID string     `json:"tool_call_id,omitempty"` // required on role=tool replies (OpenAI)

	// ReasoningContent is the non-standard field reasoning models (qwen3,
	// nemotron, gpt-oss harmony) sometimes use to ship the final response
	// while leaving Content empty. We DO NOT include it when sending the
	// message back (omitempty), and at receive time the client merges it
	// into Content when Content is empty — see mergeReasoning() below.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// mergeReasoning hoists ReasoningContent into Content when Content is
// empty. Local reasoning models (qwen3-30b on Mac, nemotron-30b, gpt-oss)
// frequently emit the final answer in reasoning_content with content="".
// Consumers reading only .Content would see empty replies otherwise.
func (m *Message) mergeReasoning() {
	if m.Content == "" && m.ReasoningContent != "" {
		m.Content = m.ReasoningContent
	}
	m.ReasoningContent = ""
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the tool name and arguments.
//
// Arguments is a custom type to bridge the OpenAI spec mismatch with
// local models: the spec says `arguments` is a JSON-encoded STRING
// (each call site then json.Unmarshals it into the tool's expected
// schema). Some lenient servers also send a raw object. Both formats
// are accepted; call sites still see map[string]any.
type ToolCallFunction struct {
	Name      string            `json:"name"`
	Arguments ToolCallArguments `json:"arguments"`
}

// ToolCallArguments accepts either a JSON-encoded string (per the
// OpenAI tool-calling spec) or a JSON object (lenient servers).
// Callers see it as map[string]any either way.
type ToolCallArguments map[string]any

// UnmarshalJSON accepts both the spec-compliant string form and the
// lenient object form. Empty string and null both decode to an empty
// map (the LLM emitted no arguments).
func (a *ToolCallArguments) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*a = ToolCallArguments{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("decoding string-encoded arguments: %w", err)
		}
		if s == "" {
			*a = ToolCallArguments{}
			return nil
		}
		m := make(map[string]any)
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			// Treat unterminated/incomplete JSON specifically so callers can
			// distinguish a model truncation (retry-friendly) from a real
			// parser bug (developer error).
			if isIncompleteJSON(err) {
				return fmt.Errorf("%w: %v (raw: %q)", ErrToolCallArgsTruncated, err, s)
			}
			return fmt.Errorf("parsing arguments JSON from string: %w", err)
		}
		*a = m
		return nil
	}
	m := make(map[string]any)
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("decoding arguments object: %w", err)
	}
	*a = m
	return nil
}

// ToolDef describes a tool for the LLM to call (OpenAI function calling format).
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function ToolDefFunc `json:"function"`
}

// ToolDefFunc is the function description inside a ToolDef.
type ToolDefFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// Chatter is implemented by any LLM backend (OpenAI-compatible HTTP, claude CLI, etc.).
type Chatter interface {
	Chat(ctx context.Context, messages []Message, temp float64, maxTokens int, onToken func(string)) (string, error)
	ChatMessage(ctx context.Context, messages []Message, temp float64, maxTokens int) (*Message, error)
	ChatWithTools(ctx context.Context, messages []Message, temp float64, maxTokens int, tools []ToolDef) (*Message, error)
	ChatSync(ctx context.Context, messages []Message, temp float64, maxTokens int) (string, error)
}

// ClaudeCodeSystemPrefix must be prepended to system prompts when using Claude Code API.
const ClaudeCodeSystemPrefix = "You are Claude Code, Anthropic's official CLI for Claude."

// Compile-time assertion that *Client satisfies Chatter.
var _ Chatter = (*Client)(nil)

// Client talks to an OpenAI-compatible LLM server.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// NewClient creates a new LLM client with API key auth.
// baseURL should be the API base (e.g. "http://localhost:11434" for Ollama,
// "https://api.groq.com/openai/v1" for Groq).
// When apiKey is non-empty, an Authorization: Bearer header is sent.
func NewClient(baseURL, model, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 5 * time.Minute, // LLMs can be slow on RPi
		},
	}
}

// setAuth sets the Authorization header on a request.
func (c *Client) setAuth(ctx context.Context, req *http.Request) error {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return nil
}

// chatEndpoint returns the chat completions URL.
// Ollama: baseURL already includes the host, append /v1/chat/completions.
// Cloud APIs: baseURL already includes /v1, append /chat/completions.
func (c *Client) chatEndpoint() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/chat/completions"
	}
	return c.baseURL + "/v1/chat/completions"
}

// openaiRequest is the OpenAI-compatible chat completions request body.
type openaiRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Tools       []ToolDef `json:"tools,omitempty"`
}

// openaiResponse is the non-streaming response body.
type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
}

type openaiChoice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// openaiStreamChunk is a single SSE chunk from a streaming response.
type openaiStreamChunk struct {
	Choices []openaiStreamChoice `json:"choices"`
}

type openaiStreamChoice struct {
	Delta        openaiDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openaiDelta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// Chat sends a conversation to the LLM and streams the response token by token.
// The token callback is called for each streamed token; the full response is also returned.
func (c *Client) Chat(ctx context.Context, messages []Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	req := openaiRequest{
		Model:       c.model,
		Messages:    messages,
		Stream:      true,
		Temperature: temp,
		MaxTokens:   maxTokens,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.chatEndpoint(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := c.setAuth(ctx, httpReq); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("LLM returned %d: reading error body: %w", resp.StatusCode, err)
		}
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(b))
	}

	return c.parseSSEStream(resp.Body, onToken)
}

// parseSSEStream reads an SSE stream (data: {...}\n) and extracts content tokens.
func (c *Client) parseSSEStream(body io.Reader, onToken func(string)) (string, error) {
	var full strings.Builder
	scanner := bufio.NewScanner(body)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: {...}" or "data: [DONE]"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			token := choice.Delta.Content
			if token == "" {
				// Reasoning-content fallback: qwen3/nemotron/gpt-oss stream
				// some final answers via reasoning_content with Content="".
				token = choice.Delta.ReasoningContent
			}
			if token != "" {
				full.WriteString(token)
				if onToken != nil {
					onToken(token)
				}
			}
		}
	}

	return full.String(), scanner.Err()
}

// ChatMessage sends a conversation and returns the full response Message including tool calls.
// Uses non-streaming mode to get the complete message with tool_calls in a single response.
func (c *Client) ChatMessage(ctx context.Context, messages []Message, temp float64, maxTokens int) (*Message, error) {
	return c.chatFull(ctx, messages, temp, maxTokens, nil)
}

// ChatWithTools sends a conversation with tool definitions and returns the response.
// The LLM may return tool_calls in the response for the caller to execute.
func (c *Client) ChatWithTools(ctx context.Context, messages []Message, temp float64, maxTokens int, tools []ToolDef) (*Message, error) {
	return c.chatFull(ctx, messages, temp, maxTokens, tools)
}

// chatFull does a non-streaming chat call and returns the full Message with tool calls.
func (c *Client) chatFull(ctx context.Context, messages []Message, temp float64, maxTokens int, tools []ToolDef) (*Message, error) {
	req := openaiRequest{
		Model:       c.model,
		Messages:    messages,
		Stream:      false,
		Temperature: temp,
		MaxTokens:   maxTokens,
		Tools:       tools,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.chatEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := c.setAuth(ctx, httpReq); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	httpReq = httpReq.WithContext(ctx)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("LLM returned %d: reading error body: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(b))
	}

	var result openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing LLM response: %w", err)
	}

	if len(result.Choices) == 0 {
		return &Message{Role: "assistant"}, nil
	}

	msg := &result.Choices[0].Message
	// Reasoning models (qwen3, nemotron, gpt-oss harmony) sometimes ship
	// the final answer in reasoning_content with content="". Merge before
	// any further processing so the rest of the pipeline sees a populated
	// Content field.
	msg.mergeReasoning()
	// Rescue inline <tool_call> XML blocks that small local models emit
	// when they violate the trained "tool call comes BEFORE prose, not
	// after" instruction. Without this, the raw XML leaks to the user as
	// visible text and the bot looks broken.
	salvageInlineToolCalls(msg)
	return msg, nil
}

// isIncompleteJSON identifies truncation-style decode errors from
// encoding/json. The stdlib returns io.ErrUnexpectedEOF or a
// *json.SyntaxError with "unexpected end of JSON input" depending on
// where in the stream the cut happened.
func isIncompleteJSON(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "unexpected end of JSON input") ||
		strings.Contains(msg, "unexpected EOF")
}

// ChatSync sends a conversation and returns the full response (non-streaming).
func (c *Client) ChatSync(ctx context.Context, messages []Message, temp float64, maxTokens int) (string, error) {
	return c.Chat(ctx, messages, temp, maxTokens, nil)
}

// HardwareRecommendation returns a model recommendation based on available RAM.
// Prefers models with native tool calling (Gemma 4, Qwen3, Phi-4).
func HardwareRecommendation(ramMB int) string {
	switch {
	case ramMB >= 16384:
		return "gemma4:e4b"
	case ramMB >= 8192:
		return "gemma4:e2b"
	case ramMB >= 4096:
		return "qwen3:4b"
	case ramMB >= 2048:
		return "phi4-mini"
	default:
		return "tinyllama"
	}
}
