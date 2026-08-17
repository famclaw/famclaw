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
	"log"
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
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content,omitempty"`
	// For multimodal content (images, etc.). If set (non-nil and non-empty),
	// takes precedence over Content when marshaling to JSON.
	ContentParts []any
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`   // present when LLM requests tool use
	ToolCallID   string     `json:"tool_call_id,omitempty"` // required on role=tool replies (OpenAI)

	// ReasoningContent is the non-standard field reasoning models (qwen3,
	// nemotron, gpt-oss harmony) sometimes use to ship the final response
	// while leaving Content empty. We DO NOT include it when sending the
	// message back (omitempty), and at receive time the client merges it
	// into Content when Content is empty — see mergeReasoning() below.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Reasoning captures the plain "reasoning" field emitted by some
	// Ollama/LiteLLM gateways (e.g. Gemma-4-26b, qwen3.6-27b) that ship the
	// final answer there while leaving content empty. Like ReasoningContent,
	// it is only used as a fallback when Content is empty and is cleared
	// after merging — see mergeReasoning() below. It is not sent back in
	// requests because MarshalJSON omits it.
	Reasoning string `json:"reasoning,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for Message.
// If ContentParts is set (non-nil and non-empty), it is used for the "content"
// field as an array (multimodal). Otherwise, the Content string field is used.
//
// Non-assistant roles (system, user, tool) always emit a "content" key, even
// when empty — llama-server rejects messages that omit it, while assistant
// messages may legitimately omit content when they carry only tool_calls.
func (m Message) MarshalJSON() ([]byte, error) {
	// If ContentParts is set and non-empty, use it
	if m.ContentParts != nil && len(m.ContentParts) > 0 {
		return json.Marshal(struct {
			Role             string     `json:"role"`
			Content          []any      `json:"content"`
			ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
			ToolCallID       string     `json:"tool_call_id,omitempty"`
			ReasoningContent string     `json:"reasoning_content,omitempty"`
		}{
			Role:             m.Role,
			Content:          m.ContentParts,
			ToolCalls:        m.ToolCalls,
			ToolCallID:       m.ToolCallID,
			ReasoningContent: m.ReasoningContent,
		})
	}

	// Non-assistant roles (system, user, tool) must always emit a "content"
	// key, even when empty. llama-server (and llama.cpp) reject requests whose
	// non-assistant messages omit content ("All non-assistant messages must
	// contain 'content'"), whereas vLLM tolerates the omission — which is why
	// this only surfaced when switching to a llama.cpp-backed model. Assistant
	// messages legitimately omit content when they carry only tool_calls, so
	// for them we keep the historical omitempty behavior. A *string lets us
	// distinguish "explicitly empty" (emit "") from "omit" (nil).
	contentPtr := &m.Content
	if m.Role == "assistant" && m.Content == "" {
		contentPtr = nil
	}

	return json.Marshal(struct {
		Role             string     `json:"role"`
		Content          *string    `json:"content,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
	}{
		Role:             m.Role,
		Content:          contentPtr,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		ReasoningContent: m.ReasoningContent,
	})
}

// mergeReasoning hoists reasoning fields into Content when Content is
// empty (or whitespace). Local reasoning models (qwen3, nemotron, gpt-oss)
// frequently emit the final answer in reasoning_content with content="".
// Some Ollama/LiteLLM gateways (Gemma-4-26b, qwen3.6-27b) go further and
// use the plain "reasoning" field for the same purpose. Consumers reading
// only .Content would see empty replies otherwise.
//
// The function distinguishes between:
// - Genuine answers: direct, factual responses that should be shown to the user
// - Chain-of-thought: structured deliberation that should NOT be shown
//
// Chain-of-thought is detected by patterns like:
// - Starting with "Thinking Process:", "Let me think", numbered steps
// - First-person future-tense deliberation ("I need to...", "I should...")
// - Tool-call planning language ("I need to use the...", "Formulate the...")
func (m *Message) mergeReasoning() {
	if strings.TrimSpace(m.Content) == "" {
		if m.ReasoningContent != "" {
			m.Content = mergeReasoningField(m.ReasoningContent)
		} else if m.Reasoning != "" {
			m.Content = mergeReasoningField(m.Reasoning)
		}
	}
	m.ReasoningContent = ""
	m.Reasoning = ""
}

// mergeReasoningField strips Gemma control tokens and hoists the
// reasoning field into Content when Content is empty. When the reasoning
// field contains Gemma control tokens (e.g. <|channel>thought...<|channel|>),
// the model is using a thinking format where the answer lives in the
// reasoning field — the stripped text is the actual answer, not
// chain-of-thought deliberation. For reasoning fields without control
// tokens we check isChainOfThought to avoid showing deliberation to users.
func mergeReasoningField(raw string) string {
	stripped := strings.TrimSpace(stripControlTokens(raw))
	if stripped == "" {
		return ""
	}
	if hasControlTokens(raw) || !isChainOfThought(raw) {
		return stripped
	}
	return ""
}

// hasControlTokens reports whether the text contains any Gemma control
// token fragments (tool_call_begin, tool_call_end, tool_call, tool_split,
// channel, the <"|"|> value delimiter, or bare/slash closing variants).
// This is a lightweight presence check — callers that need the actual
// stripped text should use stripControlTokens.
func hasControlTokens(s string) bool {
	return reControlToken.MatchString(s)
}

// isChainOfThought returns true when the text looks like structured
// deliberation rather than a final answer. Thinking models (qwen3, nemotron)
// put chain-of-thought in reasoning_content; showing it to the user reveals
// their internal monologue instead of an answer.
func isChainOfThought(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}

	// Pattern 1: Starts with deliberation markers
	deliberationPrefixes := []string{
		"thinking process:", "let me think", "step 1:", "step 2:", "step 3:",
		"next,", "then,", "after that,",
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range deliberationPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	// Pattern 2: First-person future-tense deliberation
	deliberationPatterns := []string{
		"i need to", "i should", "i will", "i have to", "i ought to",
		"i'm going to", "i'm going", "i计划",  // Chinese planning markers
		"i need use", "i need call", "i need run",  // tool planning
	}
	for _, pattern := range deliberationPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	// Pattern 3: Tool-call planning language
	toolPlanning := []string{
		"i need to use the", "i need to call", "i need to run",
		"i will use the", "i will call", "i will run",
		"formulate the", "execute the", "perform the",
		"search for", "look up",
	}
	for _, pattern := range toolPlanning {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	// Pattern 4: Numbered list with deliberation markers
	if strings.HasPrefix(trimmed, "1. **") {
		return true
	}

	return false
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

// MarshalJSON serializes arguments as a JSON-encoded string, per the
// OpenAI tool-calling spec: function.arguments must be a string, not a
// raw JSON object. Without this, the default map[string]any marshaling
// produces a bare object, which strict backends (vllm, OpenAI, Azure)
// reject with HTTP 400. This is the symmetric counterpart to
// UnmarshalJSON above.
//
// The method is declared on a VALUE receiver (not *ToolCallArguments)
// because the Arguments field is held by value in ToolCallFunction.
// encoding/json will not invoke a pointer-receiver MarshalJSON on a
// non-addressable value, which would silently emit a JSON object in
// production. A nil or empty map emits "{}" (the JSON string form of
// an empty object), never null or "", so a downstream model never
// receives null arguments.
func (a ToolCallArguments) MarshalJSON() ([]byte, error) {
	if len(a) == 0 {
		return []byte(`"{}"`), nil
	}
	inner, err := json.Marshal(map[string]any(a))
	if err != nil {
		return nil, fmt.Errorf("marshaling tool call arguments: %w", err)
	}
	return json.Marshal(string(inner))
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
	// Default timeout for LLM calls (5 minutes is too long for many operations)
	defaultTimeout time.Duration
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
		defaultTimeout: 5 * time.Minute,
	}
}

// WithTimeout sets a per-call timeout for LLM requests. The timeout applies
// to each individual Chat, ChatMessage, and ChatWithTools call via a context
// deadline. If not called, the default 5-minute timeout is used.
func (c *Client) WithTimeout(d time.Duration) *Client {
	if d > 0 {
		c.defaultTimeout = d
	}
	return c
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
	Reasoning        string     `json:"reasoning,omitempty"`
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

	// Use a configurable per-call timeout instead of the global client timeout
	ctx, cancel := context.WithTimeout(ctx, c.defaultTimeout)
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
			// Log the error for debugging while still returning the status error
			log.Printf("LLM returned %d: failed to read error body: %v", resp.StatusCode, err)
			return "", fmt.Errorf("LLM returned %d: reading error body: %w", resp.StatusCode, err)
		}
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(b))
	}

	return c.parseSSEStream(resp.Body, onToken)
}

// parseSSEStream reads an SSE stream (data: {...}\n) and extracts content tokens.
func (c *Client) parseSSEStream(body io.Reader, onToken func(string)) (string, error) {
	var full strings.Builder
	var carry string
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
			log.Printf("[llm] sstream: skipping malformed chunk: %v", err)
			continue
		}

		for _, choice := range chunk.Choices {
			token := choice.Delta.Content
			if token == "" {
				// Reasoning-content fallback: qwen3/nemotron/gpt-oss stream
				// some final answers via reasoning_content or the plain
				// "reasoning" field (Gemma-4-26b, qwen3.6-27b via LiteLLM)
				// with Content="".
				token = choice.Delta.ReasoningContent
				if token == "" {
					token = choice.Delta.Reasoning
				}
			}
			if token != "" {
				// stripWithCarry handles control tokens that may be
				// split across SSE chunks: it holds back a tail long
				// enough to cover the longest known Gemma token and
				// prepends it to the next chunk before stripping.
				emit, newCarry := stripWithCarry(carry, token)
				carry = newCarry
				if emit != "" {
					full.WriteString(emit)
					if onToken != nil {
						onToken(emit)
					}
				}
			}
		}
	}

	// Flush any remaining carry at stream end. A split token is now
	// complete and will be stripped by stripControlTokens.
	if carry != "" {
		cleaned := stripControlTokens(carry)
		if cleaned != "" {
			full.WriteString(cleaned)
			if onToken != nil {
				onToken(cleaned)
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

	// Use a configurable per-call timeout instead of the global client timeout
	ctx, cancel := context.WithTimeout(ctx, c.defaultTimeout)
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
		return nil, fmt.Errorf("LLM returned no choices")
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
	// Belt-and-braces: strip any remaining Gemma control tokens
	// so that no model-internal token format can ever reach the user,
	// even if a new unrecognised variant appears.
	msg.Content = strings.TrimSpace(stripControlTokens(msg.Content))
	// An empty reply is never acceptable — the family must never see
	// a blank message. If the model produced no content AND no tool
	// calls, return an honest error instead of a silent void.
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
		return nil, fmt.Errorf("LLM produced an empty response with no tool calls")
	}
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
//
// Picks are from the community default benchmark (see
// data/fc-model-benchmark/report.md): only qwen3-family models with proven
// native tool calling. gemma4:e4b (8.9 GB) is excluded because it cannot fit
// an 8 GB Pi 5, gemma4:e2b is borderline, and phi4-mini is excluded because
// it fakes tool calls (0/3). qwen3:1.7b (1.3 GB) is the Pi-class default;
// qwen3:4b is offered when there is comfortable headroom (16 GB+).
func HardwareRecommendation(ramMB int) string {
	switch {
	case ramMB >= 16384:
		// 16 GB+: qwen3:4b (2.3 GB) fits with comfortable headroom and is
		// the benchmark's richer-prose runner-up.
		return "qwen3:4b"
	case ramMB >= 8192:
		// 8-16 GB: Pi 5 class — qwen3:1.7b (1.3 GB, ~2 GB RAM) is the
		// benchmark-proven default with real tool calls.
		return "qwen3:1.7b"
	case ramMB >= 2048:
		// 2-8 GB: qwen3:1.7b still fits (~2 GB) with headroom, so prefer it
		// over the larger qwen3:4b to avoid OOM on constrained RAM.
		return "qwen3:1.7b"
	default:
		// < 2 GB: no qwen3 tier fits comfortably; fall back to a tiny model.
		return "tinyllama"
	}
}
