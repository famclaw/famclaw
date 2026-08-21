package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// coTAnswer is a chain-of-thought shaped reasoning payload carrying a
// structural CoT marker (thinking tags); the model-aware gate must NOT
// hoist it, but a gateway merge=true flag must force-hoist it.
const coTAnswer = "<thinkThe user wants the weather. I need to use the web_search tool to find it.\n" +
	"</think>"

// genuineAnswer looks like a final reply, not deliberation.
const genuineAnswer = "Anthropic is a private company, so it has no share price."

// mockGateway serves /v1/model/info with the given model→flag table.
func mockGateway(flags map[string]*bool) *httptest.Server {
	type entry struct {
		ModelName     string `json:"model_name"`
		LitellmParams struct {
			MergeReasoning *bool `json:"merge_reasoning_content_in_choices"`
		} `json:"litellm_params"`
	}
	entries := make([]entry, 0, len(flags))
	for model, flag := range flags {
		var e entry
		e.ModelName = model
		e.LitellmParams.MergeReasoning = flag
		entries = append(entries, e)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": entries})
	}))
}

// mockChat returns a chat server that emits the given non-streaming body
// for /v1/chat/completions.
func mockChat(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Write([]byte(body))
			return
		}
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}))
}

func TestMergeReasoningWithAutoDetect(t *testing.T) {
	yes, no := true, false
	gateway := mockGateway(map[string]*bool{
		"council":     &yes, // gemma style: reasoning field IS the answer
		"smart":       &no,  // qwen3.6 style: reasoning field is chain-of-thought
		"qwen3.6-35b": &yes, // gateway says: this deployment's reasoning field is the answer
	})
	defer gateway.Close()

	cache := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: gateway.URL})
	// Load once so the tests exercise cache hits, not the fetch path.
	if _, err := cache.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	tests := []struct {
		name        string
		model       string
		reasoning   string // response body's reasoning_content
		content     string
		wantContent string
		wantErr     bool
	}{
		{
			name:        "flag true: answer in reasoning is hoisted",
			model:       "council",
			reasoning:   genuineAnswer,
			wantContent: genuineAnswer,
		},
		{
			name:        "flag true: even CoT-looking text is hoisted (operator setting wins)",
			model:       "council",
			reasoning:   coTAnswer,
			wantContent: coTAnswer,
		},
		{
			name:      "flag false: chain-of-thought is dropped",
			model:     "smart",
			reasoning: coTAnswer,
			wantErr:   true, // empty content, no tool calls → honest error
		},
		{
			name:      "flag false: reasoning kept private even when it looks like an answer (operator setting wins)",
			model:     "smart",
			reasoning: genuineAnswer,
			wantErr:   true,
		},
		{
			name:        "content always wins over reasoning",
			model:       "council",
			content:     "68",
			reasoning:   "Let me compute 17*4. 17*4 = 68.",
			wantContent: "68",
		},
		{
			name:      "unlisted model falls back to the model-aware policy",
			model:     "mystery",
			reasoning: coTAnswer,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":              "assistant",
						"content":           tt.content,
						"reasoning_content": tt.reasoning,
					},
					"finish_reason": "stop",
				}},
			}
			buf := new(strings.Builder)
			_ = json.NewEncoder(buf).Encode(resp)
			chat := mockChat(buf.String())
			defer chat.Close()

			client := NewClient(chat.URL, tt.model, "").WithReasoningCache(cache)
			msg, err := client.ChatMessage(context.Background(), []Message{{Role: "user", Content: "hi"}}, 0.7, 100)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got content %q", msg.Content)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatMessage: %v", err)
			}
			if msg.Content != tt.wantContent {
				t.Fatalf("Content = %q, want %q", msg.Content, tt.wantContent)
			}
			if msg.ReasoningContent != "" || msg.Reasoning != "" {
				t.Fatal("reasoning fields must be cleared after merging")
			}
		})
	}
}

func TestMergeReasoningCacheAbsentKeepsModelAwarePolicy(t *testing.T) {
	// No cache attached at all: the model-aware policy decides.
	resp := `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":` +
		`"` + strings.ReplaceAll(genuineAnswer, `"`, `\"`) + `"},"finish_reason":"stop"}]}`
	chat := mockChat(resp)
	defer chat.Close()

	client := NewClient(chat.URL, "council", "")
	msg, err := client.ChatMessage(context.Background(), []Message{{Role: "user", Content: "hi"}}, 0.7, 100)
	if err != nil {
		t.Fatalf("ChatMessage: %v", err)
	}
	if msg.Content != genuineAnswer {
		t.Fatalf("Content = %q, want %q", msg.Content, genuineAnswer)
	}
}

// sseReply builds an SSE stream body from (content, reasoning) token pairs.
func sseReply(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(`data: ` + t + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestStreamReasoningWithAutoDetect(t *testing.T) {
	yes, no := true, false
	gateway := mockGateway(map[string]*bool{"council": &yes, "smart": &no, "qwen3.6-35b": &yes})
	defer gateway.Close()

	cache := NewReasoningCache(ReasoningAutoDetectConfig{Enabled: true, GatewayBaseURL: gateway.URL})
	if _, err := cache.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	tests := []struct {
		name    string
		model   string
		stream  string
		want    string
		wantErr bool
		noCache bool
	}{
		{
			name:  "flag true: reasoning-only answer is hoisted at stream end",
			model: "council",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"Hello "}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"family."}}]}`,
			),
			want: "Hello family.",
		},
		{
			name:  "flag false: CoT side to content is dropped",
			model: "smart",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":"It is sunny."}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"Thinking Process:\n1. I need to "}}]}`,
			),
			want: "It is sunny.",
		},
		{
			name:  "flag false: reasoning-only payload is kept private",
			model: "smart",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":`+jsonToken(genuineAnswer)+`}}]}`,
			),
			want: "",
		},
		{
			name:  "no cache: unknown flag buffers, model-aware policy hoists genuine answer",
			model: "council",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"Hello "}}]}`,
				`{"choices":[{"delta":{"reasoning_content":"family."}}]}`,
			),
			want:    "Hello family.",
			noCache: true,
		},
		{
			name:  "flag false: CoT-only stream stays private",
			model: "smart",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":`+jsonToken(coTAnswer)+`}}]}`,
			),
			want: "",
		},
		{
			name:  "no cache: CoT-only stream stays private",
			model: "council",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":`+jsonToken(coTAnswer)+`}}]}`,
			),
			want:    "",
			noCache: true,
		},
		{
			name:  "flag true: answer hoisted even for a thinking-family model name",
			model: "qwen3.6-35b",
			stream: sseReply(
				`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"delta":{"reasoning_content":`+jsonToken(genuineAnswer)+`}}]}`,
			),
			want: genuineAnswer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte(tt.stream))
			}))
			defer chat.Close()

			client := NewClient(chat.URL, tt.model, "")
			if !tt.noCache {
				client = client.WithReasoningCache(cache)
			}

			out, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, 0.7, 100, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got output %q", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if out != tt.want {
				t.Fatalf("stream output = %q, want %q", out, tt.want)
			}
		})
	}
}

func jsonToken(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
