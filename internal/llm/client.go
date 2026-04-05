// Package llm provides a client for LLM inference via any OpenAI-compatible API.
// Works with: Ollama (v0.1.24+), llama.cpp server, Groq, OpenAI, OpenRouter.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a conversation turn.
type Message struct {
	Role      string     `json:"role"`                // system | user | assistant | tool
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // present when LLM requests tool use
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the tool name and arguments.
type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
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

// Client talks to an OpenAI-compatible LLM server.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// NewClient creates a new LLM client.
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
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
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
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(b))
	}

	var result openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing LLM response: %w", err)
	}

	if len(result.Choices) == 0 {
		return &Message{Role: "assistant"}, nil
	}

	return &result.Choices[0].Message, nil
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
