package llm

import (
	"strings"
	"testing"
)

// TestParseSSEStreamReasoning pins the streaming CoT behavior: answer tokens
// (delta.content) stream live, while reasoning fields (delta.reasoning_content
// / delta.reasoning) are buffered and reconciled model-aware at the end. A
// genuine answer in the reasoning field is hoisted; deliberation on known
// thinking models and structurally marked CoT elsewhere is suppressed, so
// the user never sees raw deliberation mid-stream.
func TestParseSSEStreamReasoning(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		sse       string
		wantFinal string
		wantLive  []string
	}{
		{
			name:  "content streams live, reasoning is dropped when content present",
			model: "qwen3.8",
			sse: `data: {"choices":[{"delta":{"reasoning":"Let me think about this..."},"role":"assistant"}]}
data: {"choices":[{"delta":{"content":"The weather is sunny."}}]}
data: [DONE]
`,
			wantFinal: "The weather is sunny.",
			wantLive:  []string{"The weather is sunny."},
		},
		{
			name:  "gemma english answer in reasoning_content is hoisted at end",
			model: "gemma-4-26b",
			sse: `data: {"choices":[{"delta":{"reasoning_content":"Anthropic is a private company, so it has no share price."}}]}
data: [DONE]
`,
			wantFinal: "Anthropic is a private company, so it has no share price.",
			wantLive:  []string{"Anthropic is a private company, so it has no share price."},
		},
		{
			name:  "gemma non-english answer in reasoning_content is hoisted at end",
			model: "gemma-4-26b",
			sse: `data: {"choices":[{"delta":{"reasoning_content":"La réponse est 42."}}]}
data: [DONE]
`,
			wantFinal: "La réponse est 42.",
			wantLive:  []string{"La réponse est 42."},
		},
		{
			name:  "qwen english CoT in reasoning_content is suppressed",
			model: "qwen3.8",
			sse: `data: {"choices":[{"delta":{"reasoning_content":"Thinking Process:\n1. **Identify the question**\n2. **Formulate a query**"}}]}
data: [DONE]
`,
			wantFinal: "",
			wantLive:  nil,
		},
		{
			name:  "qwen french CoT in reasoning_content is suppressed",
			model: "qwen3.8",
			sse: `data: {"choices":[{"delta":{"reasoning_content":"Processus de réflexion :\n1. **Identifier la question principale**"}}]}
data: [DONE]
`,
			wantFinal: "",
			wantLive:  nil,
		},
		{
			name:  "unknown model thinking-tag CoT in reasoning is suppressed",
			model: "some-model",
			sse: `data: {"choices":[{"delta":{"reasoning":"<thinking> Ich überlege schrittweise..."}}]}
data: [DONE]
`,
			wantFinal: "",
			wantLive:  nil,
		},
		{
			name:  "unknown model non-english answer in reasoning is hoisted",
			model: "some-model",
			sse: `data: {"choices":[{"delta":{"reasoning":"Die Antwort ist 42."}}]}
data: [DONE]
`,
			wantFinal: "Die Antwort ist 42.",
			wantLive:  []string{"Die Antwort ist 42."},
		},
		{
			name:  "reasoning_content takes precedence over reasoning (no concat)",
			model: "gemma-4-26b",
			sse: `data: {"choices":[{"delta":{"reasoning_content":"The answer is 42."}}]}
data: {"choices":[{"delta":{"reasoning":"Ich überlege..."}}]}
data: [DONE]
`,
			wantFinal: "The answer is 42.",
			wantLive:  []string{"The answer is 42."},
		},
		{
			name:  "plain content only, no reasoning",
			model: "qwen3.8",
			sse: `data: {"choices":[{"delta":{"content":"Hello "}}]}
data: {"choices":[{"delta":{"content":"there"}}]}
data: [DONE]
`,
			// stripWithCarry holds back a chunk-boundary tail long enough
			// to cover a split control token, so both chunks surface as one
			// flushed emission at stream end.
			wantFinal: "Hello there",
			wantLive:  []string{"Hello there"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient("http://localhost:11434", tt.model, "")
			var live []string
			onToken := func(tok string) { live = append(live, tok) }
			final, err := c.parseSSEStream(strings.NewReader(tt.sse), onToken)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if final != tt.wantFinal {
				t.Fatalf("final = %q, want %q", final, tt.wantFinal)
			}
			if len(live) != len(tt.wantLive) {
				t.Fatalf("live tokens = %v, want %v", live, tt.wantLive)
			}
			for i := range tt.wantLive {
				if live[i] != tt.wantLive[i] {
					t.Fatalf("live token[%d] = %q, want %q", i, live[i], tt.wantLive[i])
				}
			}
		})
	}
}
