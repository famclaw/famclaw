// Package llm provides a client for local LLM inference via Ollama.
// Ollama runs on RPi 4/5, Mac, and Linux. For older hardware use tinyllama.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the tool name and arguments.
type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Client talks to an Ollama-compatible local LLM server.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

// NewClient creates a new LLM client.
// When apiKey is non-empty, an Authorization: Bearer header is sent.
// When apiKey is empty, no Authorization header is sent (Ollama doesn't need it,
// and some proxies reject unexpected headers).
func NewClient(baseURL, model, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 5 * time.Minute, // LLMs can be slow on RPi
		},
	}
}

// ollamaRequest is the Ollama /api/chat request body.
type ollamaRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Options  ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaChunk struct {
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done bool `json:"done"`
}

// Chat sends a conversation to the LLM and streams the response token by token.
// The token callback is called for each streamed token; the full response is also returned.
func (c *Client) Chat(ctx context.Context, messages []Message, temp float64, maxTokens int, onToken func(string)) (string, error) {
	req := ollamaRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   true,
		Options: ollamaOptions{
			Temperature: temp,
			NumPredict:  maxTokens,
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, string(b))
	}

	var full string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chunk ollamaChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue
		}
		token := chunk.Message.Content
		full += token
		if onToken != nil && token != "" {
			onToken(token)
		}
		if chunk.Done {
			break
		}
	}

	return full, scanner.Err()
}

// ChatMessage sends a conversation and returns the full response Message including tool calls.
// Uses non-streaming mode to get the complete message with tool_calls in a single response.
func (c *Client) ChatMessage(ctx context.Context, messages []Message, temp float64, maxTokens int) (*Message, error) {
	return c.chatFull(ctx, messages, temp, maxTokens)
}

// chatFull does a non-streaming chat call and returns the full Message with tool calls.
func (c *Client) chatFull(ctx context.Context, messages []Message, temp float64, maxTokens int) (*Message, error) {
	req := ollamaRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   false,
		Options:  ollamaOptions{Temperature: temp, NumPredict: maxTokens},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Message Message `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &result.Message, nil
}

// ChatSync sends a conversation and returns the full response (non-streaming).
func (c *Client) ChatSync(ctx context.Context, messages []Message, temp float64, maxTokens int) (string, error) {
	return c.Chat(ctx, messages, temp, maxTokens, nil)
}

// Ping checks if the Ollama server is reachable and the model is available.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama not reachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil // server is up, just can't parse response
	}

	for _, m := range result.Models {
		if m.Name == c.model || m.Name == c.model+":latest" {
			return nil
		}
	}
	return fmt.Errorf("model %q not found in ollama — run: ollama pull %s", c.model, c.model)
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
