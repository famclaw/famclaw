package llm

import "testing"

// TestMergeReasoningAutoDetect pins the behaviour mergeReasoning SHOULD have.
//
// Today it hoists the reasoning field into Content whenever Content is empty.
// That is right for models like gemma-4-26b, which ship their FINAL ANSWER in
// reasoning_content. It is wrong for thinking models like qwen3.6, where that
// field holds raw chain-of-thought - hoisting it shows the family the model's
// internal monologue instead of an answer, which is what happens in production.
//
// The fix must distinguish the two cases rather than hoisting blindly.
func TestMergeReasoningAutoDetect(t *testing.T) {
	t.Run("answer in reasoning is hoisted", func(t *testing.T) {
		// gemma shape: the reasoning field IS the reply.
		m := &Message{Content: "", ReasoningContent: "Anthropic is a private company, so it has no share price."}
		m.mergeReasoning()
		if m.Content == "" {
			t.Fatal("a genuine answer in reasoning_content must still be hoisted into Content")
		}
	})

	t.Run("chain-of-thought is NOT hoisted", func(t *testing.T) {
		// qwen3.6 shape: first-person deliberation about the task, not a reply.
		cot := "Thinking Process:\n1. **Identify the user's core question:** The user wants " +
			"to know the weather.\n2. **Determine the required tool:** I need to use the " +
			"web_search tool.\n3. **Formulate the search query.**\n4. **Execute the tool call.**"
		m := &Message{Content: "", ReasoningContent: cot}
		m.mergeReasoning()
		if m.Content != "" {
			t.Fatalf("chain-of-thought must NOT be shown to the user, got %d chars: %.80q",
				len(m.Content), m.Content)
		}
	})

	t.Run("real content is never overwritten", func(t *testing.T) {
		m := &Message{Content: "68", ReasoningContent: "Let me compute 17*4. 17*4 = 68."}
		m.mergeReasoning()
		if m.Content != "68" {
			t.Fatalf("existing Content must win, got %q", m.Content)
		}
	})

	t.Run("reasoning fields are always cleared", func(t *testing.T) {
		m := &Message{Content: "hi", ReasoningContent: "x", Reasoning: "y"}
		m.mergeReasoning()
		if m.ReasoningContent != "" || m.Reasoning != "" {
			t.Fatal("reasoning fields must be cleared after merging")
		}
	})
}
