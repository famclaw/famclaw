package llm

import (
	"testing"
)

// Language coverage fixtures: chain-of-thought samples and legitimate final
// answers in non-English languages. Model-class detection must suppress CoT
// regardless of language and pass legitimate answers regardless of language.
const (
	cotEN = "Thinking Process:\n1. **Identify the question**\n2. **Formulate a query**"
	cotFR = "Processus de réflexion :\n1. **Identifier la question principale**\n2. **Formuler la requête de recherche**"
	cotDE = "Ich muss überlegen, welche Information gesucht wird. Ich rufe das Werkzeug web_search auf."
	cotZH = "让我思考一下。我需要确定用户想要什么，然后调用 web_search 工具。"
	cotRU = "Процесс мышления: 1. Определить вопрос. 2. Сформулировать запрос."

	answerEN = "Anthropic is a private company, so it has no share price."
	answerDE = "Anthropic ist ein privates Unternehmen, daher hat es keinen Börsenkurs."
	answerFR = "La réponse est 42."
	answerZH = "答案是 42。"
	answerGR = "Η απάντηση είναι 42."
)

// TestReasoningClassForModel pins the model-family classification that
// drives mergeReasoning: known thinking models keep reasoning private,
// everything else hoists with the structural CoT gate.
func TestReasoningClassForModel(t *testing.T) {
	tests := []struct {
		model string
		want  reasoningClass
	}{
		{"qwen3.8", classDeliberation},
		{"qwen3.6", classDeliberation},
		{"qwen3:30b", classDeliberation},
		{"Qwen3-30B-A3B-Instruct-2507", classDeliberation},
		{"nemotron-30b", classDeliberation},
		{"nvidia/nemotron-nano-9b-v2", classDeliberation},
		{"gpt-oss-20b", classDeliberation},
		{"gpt-oss:20b", classDeliberation},
		{"gemma-4-26b", classAnswerInReasoning},
		{"gemma4:e2b", classAnswerInReasoning},
		{"llama-3.1-8b", classAnswerInReasoning},
		{"qwen2.5-7b", classAnswerInReasoning},
		{"test-model", classAnswerInReasoning},
		{"", classAnswerInReasoning},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := reasoningClassForModel(tt.model); got != tt.want {
				t.Fatalf("reasoningClassForModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestMergeReasoningModelAware pins the model-aware merge policy: known
// thinking models never hoist reasoning (in any language), answer-in-
// reasoning and unknown models hoist with a language-agnostic structural
// CoT gate.
func TestMergeReasoningModelAware(t *testing.T) {
	tests := []struct {
		name  string
		model string
		msg   Message
		want  string
	}{
		// --- Known thinking models: reasoning is never hoisted ---
		{
			name:  "qwen3.8 english CoT is not hoisted",
			model: "qwen3.8",
			msg:   Message{ReasoningContent: cotEN},
		},
		{
			name:  "qwen3.8 french CoT is not hoisted",
			model: "qwen3.8",
			msg:   Message{ReasoningContent: cotFR},
		},
		{
			name:  "nemotron german CoT is not hoisted",
			model: "nemotron-30b",
			msg:   Message{Reasoning: cotDE},
		},
		{
			name:  "gpt-oss chinese CoT is not hoisted",
			model: "gpt-oss-20b",
			msg:   Message{ReasoningContent: cotZH},
		},
		{
			name:  "qwen3 russian CoT is not hoisted",
			model: "qwen3:30b",
			msg:   Message{Reasoning: cotRU},
		},
		{
			name:  "thinking model never hoists even answer-like text",
			model: "qwen3.8",
			msg:   Message{ReasoningContent: answerDE},
		},
		{
			name:  "qwen3 real content wins, reasoning cleared",
			model: "qwen3.8",
			msg:   Message{Content: "68", ReasoningContent: "Let me compute 17*4. 17*4 = 68."},
			want:  "68",
		},

		// --- Answer-in-reasoning models (gemma) ---
		{
			name:  "gemma english answer in reasoning is hoisted",
			model: "gemma-4-26b",
			msg:   Message{ReasoningContent: answerEN},
			want:  answerEN,
		},
		{
			name:  "gemma german answer in reasoning is hoisted",
			model: "gemma-4-26b",
			msg:   Message{ReasoningContent: answerDE},
			want:  answerDE,
		},
		{
			name:  "gemma channel control tokens are stripped and hoisted",
			model: "gemma-4-26b",
			msg:   Message{ReasoningContent: "<|channel>Anthropic ist privat.<|channel|>"},
			want:  "Anthropic ist privat.",
		},
		{
			name:  "existing content is never overwritten",
			model: "gemma-4-26b",
			msg:   Message{Content: "68", ReasoningContent: "Let me compute 17*4. 17*4 = 68."},
			want:  "68",
		},

		// --- Unknown models: structural gate only ---
		{
			name:  "unknown model french answer passes",
			model: "some-local-model",
			msg:   Message{ReasoningContent: answerFR},
			want:  answerFR,
		},
		{
			name:  "unknown model greek answer passes",
			model: "some-local-model",
			msg:   Message{ReasoningContent: answerGR},
			want:  answerGR,
		},
		{
			name:  "unknown model chinese answer passes",
			model: "some-local-model",
			msg:   Message{Reasoning: answerZH},
			want:  answerZH,
		},
		{
			name:  "unknown model thinking-tag CoT is suppressed",
			model: "some-local-model",
			msg:   Message{Reasoning: "Ich überlege schrittweise, welche Information gesucht wird.</thinking>"},
		},
		{
			// French deliberation with no English keywords: suppression must
			// come from the structural thinking tags, not word patterns.
			name:  "unknown model french CoT with thinking tags is suppressed",
			model: "some-local-model",
			msg:   Message{Reasoning: "<thinking>" + " Processus de réflexion : d'abord identifier la question, ensuite formuler la requête." + "</thinking>"},
		},
		{
			name:  "unknown model trace before final-answer separator is suppressed",
			model: "some-local-model",
			msg:   Message{ReasoningContent: "D'abord, je cherche l'information.\nEnsuite, je vérifie.\nFINAL ANSWER: 42"},
		},
		{
			name:  "unknown model bare final-answer line is kept",
			model: "some-local-model",
			msg:   Message{ReasoningContent: "FINAL ANSWER: 42"},
			want:  "FINAL ANSWER: 42",
		},
		{
			name:  "unknown model defensive precedence: structural CoT in reasoning_content, answer in reasoning",
			model: "some-local-model",
			msg: Message{
				ReasoningContent: "Je cherche l'information.\nFINAL ANSWER: 42",
				Reasoning:        answerFR,
			},
			want: answerFR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.msg
			m.mergeReasoning(tt.model)
			if m.Content != tt.want {
				t.Fatalf("Content = %q, want %q", m.Content, tt.want)
			}
			if m.ReasoningContent != "" || m.Reasoning != "" {
				t.Fatalf("reasoning fields must be cleared, got RC=%q R=%q", m.ReasoningContent, m.Reasoning)
			}
		})
	}
}

// TestIsChainOfThoughtStructural pins that the CoT gate is structural
// (language-agnostic) only: natural-language deliberation phrases carry no
// weight, structural markers do.
func TestIsChainOfThoughtStructural(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"english CoT phrase is no longer special", cotEN, false},
		{"english tool-planning phrase is no longer special", "I need to use the web_search tool to find the answer.", false},
		{"french CoT phrase is not special", cotFR, false},
		{"opening thinking tag", "<thinking> Je réfléchis...", true},
		{"closing thinking tag", "Ich überlege...</thinking>", true},
		{"trace before final-answer separator", "Let me check the logs.\nFINAL ANSWER: restart the service", true},
		{"bare final-answer line", "FINAL ANSWER: 42", false},
		{"french answer", answerFR, false},
		{"chinese answer", answerZH, false},
		{"russian answer", "Ответ: 42.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChainOfThought(tt.in); got != tt.want {
				t.Fatalf("isChainOfThought(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
