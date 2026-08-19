package agentcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

// mockStageCall records a single ChatWithTools invocation.
type mockStageCall struct {
	maxTokens int
	tools     []llm.ToolDef
	msgs      []llm.Message
}

// mockStageChatter is a Chatter that records ChatWithTools calls and
// distinguishes the vision describe step (nil tools) from the tool step.
// It also records Chat (streaming) calls so the no-tools path can be
// verified.
type mockStageChatter struct {
	mu             sync.Mutex
	withToolsCalls []mockStageCall
	chatCalled     bool
	describeResp   string
	describeErr    error
	toolResp       *llm.Message
	chatResp       string
}

func (m *mockStageChatter) Chat(_ context.Context, _ []llm.Message, _ float64, _ int, _ func(string)) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chatCalled = true
	return m.chatResp, nil
}

func (m *mockStageChatter) ChatMessage(_ context.Context, _ []llm.Message, _ float64, _ int) (*llm.Message, error) {
	return nil, errors.New("ChatMessage should not be called")
}

func (m *mockStageChatter) ChatSync(_ context.Context, _ []llm.Message, _ float64, _ int) (string, error) {
	return "", errors.New("ChatSync should not be called")
}

func (m *mockStageChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, maxTokens int, tools []llm.ToolDef) (*llm.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.withToolsCalls = append(m.withToolsCalls, mockStageCall{maxTokens: maxTokens, tools: tools, msgs: msgs})
	if len(tools) == 0 {
		if m.describeErr != nil {
			return nil, m.describeErr
		}
		return &llm.Message{Role: "assistant", Content: m.describeResp}, nil
	}
	return m.toolResp, nil
}

// imageMsg is a shared user message carrying a neutral synthetic image
// attachment alongside text.
func imageMsg(text string) Message {
	return Message{
		Role:    "user",
		Content: text,
		ContentParts: []any{
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBtb2Nr"}},
		},
	}
}

func TestStageLLMCall_VisionTwoStep(t *testing.T) {
	inventoryTool := []Tool{{Name: "builtin__add_inventory_item", Description: "add item", InputSchema: map[string]any{"type": "object"}}}
	toolResp := &llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID: "call_1",
			Function: llm.ToolCallFunction{
				Name:      "add_inventory_item",
				Arguments: map[string]any{"name": "square", "colour": "Orange"},
			},
		}},
	}

	tests := []struct {
		name               string
		tools              []Tool
		messages           []Message
		describeResp       string
		chatResp           string
		wantChatCalled     bool
		wantWithToolsCalls int
		// Assertions
		wantDescribeNoTools bool           // first ChatWithTools call must have nil tools (describe step)
		wantImageStripped   bool           // tool-step messages must have no image
		wantDescInToolStep  bool           // tool-step messages must contain the description text
		wantToolArgs        map[string]any // if non-nil, verify pending tool-call args match
	}{
		{
			name:                "image and tools two-step",
			tools:               inventoryTool,
			messages:            []Message{{Role: "system", Content: "sys"}, imageMsg("add this to my inventory")},
			describeResp:        "Orange square",
			wantChatCalled:      false,
			wantWithToolsCalls:  2,
			wantDescribeNoTools: true,
			wantImageStripped:   true,
			wantDescInToolStep:  true,
			wantToolArgs:        map[string]any{"name": "square", "colour": "Orange"},
		},
		{
			name:                "image and no tools single streaming call",
			tools:               nil,
			messages:            []Message{{Role: "system", Content: "sys"}, imageMsg("what is in this picture")},
			chatResp:            "I see an orange square.",
			wantChatCalled:      true,
			wantWithToolsCalls:  0,
			wantDescribeNoTools: false,
		},
		{
			name:                "tools and no image single call",
			tools:               []Tool{{Name: "builtin__echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}},
			messages:            []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "say hi"}},
			wantChatCalled:      false,
			wantWithToolsCalls:  1,
			wantDescribeNoTools: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockStageChatter{
				describeResp: tt.describeResp,
				toolResp:     toolResp,
				chatResp:     tt.chatResp,
			}
			turn := &Turn{
				User:     &config.UserConfig{Name: "parent", Role: "parent"},
				Tools:    tt.tools,
				Messages: tt.messages,
			}
			stage := NewStageLLMCall(LLMCallDeps{
				ClientFactory: func(*Turn) llm.Chatter { return mock },
				Temperature:   0.7,
				MaxTokens:     512,
			})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("stage: %v", err)
			}
			if len(mock.withToolsCalls) != tt.wantWithToolsCalls {
				t.Errorf("ChatWithTools calls = %d, want %d", len(mock.withToolsCalls), tt.wantWithToolsCalls)
			}
			if mock.chatCalled != tt.wantChatCalled {
				t.Errorf("Chat called = %v, want %v", mock.chatCalled, tt.wantChatCalled)
			}

			// The describe step must be sent without tools and with a
			// fixed budget of visionDescMaxTokens regardless of caller.
			if tt.wantDescribeNoTools {
				if len(mock.withToolsCalls) < 1 {
					t.Fatal("expected at least 1 ChatWithTools call")
				}
				first := mock.withToolsCalls[0]
				if len(first.tools) != 0 {
					t.Errorf("describe step had %d tools, want 0 (must be sent without tools)", len(first.tools))
				}
				if first.maxTokens != visionDescMaxTokens {
					t.Errorf("describe step maxTokens = %d, want %d (fixed budget, independent of caller)", first.maxTokens, visionDescMaxTokens)
				}
				if !hasImageParts(first.msgs) {
					t.Error("describe step messages must contain the image")
				}
				// The describe-step user message must lead with the instruction.
				userIdx := len(first.msgs) - 1
				for i, m := range first.msgs {
					if m.Role == "user" && len(m.ContentParts) > 0 {
						userIdx = i
						break
					}
				}
				firstPart, ok := first.msgs[userIdx].ContentParts[0].(map[string]any)
				if !ok {
					t.Fatalf("describe user msg part 0 is not a map: %T", first.msgs[userIdx].ContentParts[0])
				}
				if firstPart["type"] != "text" {
					t.Errorf("describe user msg part 0 type = %q, want text", firstPart["type"])
				}
				if txt, _ := firstPart["text"].(string); !strings.Contains(txt, "Describe") {
					t.Errorf("describe user msg should lead with instruction, got: %s", txt)
				}
			}

			// The tool step must not carry the image and must include the description.
			if tt.wantImageStripped && len(mock.withToolsCalls) >= 2 {
				second := mock.withToolsCalls[1]
				if hasImageParts(second.msgs) {
					t.Error("tool-step messages must NOT contain the image")
				}
				if tt.wantDescInToolStep {
					found := false
					for _, m := range second.msgs {
						if strings.Contains(m.Content, tt.describeResp) {
							found = true
							break
						}
					}
					if !found {
						t.Error("tool-step messages must contain the description text")
					}
				}
				// The tool step itself must have tools (not a describe step).
				if len(second.tools) == 0 {
					t.Error("tool step must have tools (second call should not be a describe step)")
				}
			}

			// Verify the tool call carried data derived from the image.
			if tt.wantToolArgs != nil {
				pending, ok := turn.GetMeta("pending_tool_calls")
				if !ok {
					t.Fatal("expected pending_tool_calls metadata")
				}
				calls, ok := pending.([]llm.ToolCall)
				if !ok || len(calls) != 1 {
					t.Fatalf("expected 1 pending tool call, got %v", pending)
				}
				if calls[0].Function.Name != "add_inventory_item" {
					t.Errorf("tool name = %q, want add_inventory_item", calls[0].Function.Name)
				}
				for k, v := range tt.wantToolArgs {
					if got := calls[0].Function.Arguments[k]; fmt.Sprintf("%v", got) != fmt.Sprintf("%v", v) {
						t.Errorf("tool arg %s = %v, want %v", k, got, v)
					}
				}
			}
		})
	}
}

// TestStageLLMCall_VisionDescribeFixedBudget verifies that the describe
// step always uses visionDescMaxTokens (1000), regardless of the caller's
// MaxTokens. A large caller budget (4096) must be capped — never balloon
// spend. This guards the captain's limited-VRAM concern.
func TestStageLLMCall_VisionDescribeFixedBudget(t *testing.T) {
	tests := []struct {
		name      string
		callerMax int
	}{
		{"caller asks 4096 → capped at 1000", 4096},
		{"caller asks 100 → still 1000 (not floored, not min)", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockStageChatter{
				describeResp: "A red ball",
				toolResp: &llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID:       "call_1",
						Function: llm.ToolCallFunction{Name: "echo", Arguments: map[string]any{"text": "red ball"}},
					}},
				},
			}
			turn := &Turn{
				User:     &config.UserConfig{Name: "parent", Role: "parent"},
				Tools:    []Tool{{Name: "builtin__echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}},
				Messages: []Message{{Role: "system", Content: "sys"}, imageMsg("hi")},
			}
			stage := NewStageLLMCall(LLMCallDeps{
				ClientFactory: func(*Turn) llm.Chatter { return mock },
				Temperature:   0.7,
				MaxTokens:     tt.callerMax,
			})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatalf("stage: %v", err)
			}
			if len(mock.withToolsCalls) != 2 {
				t.Fatalf("expected 2 ChatWithTools calls, got %d", len(mock.withToolsCalls))
			}
			describeCall := mock.withToolsCalls[0]
			if describeCall.maxTokens != visionDescMaxTokens {
				t.Errorf("describe step maxTokens = %d, want %d (fixed budget regardless of caller MaxTokens=%d)", describeCall.maxTokens, visionDescMaxTokens, tt.callerMax)
			}
			if len(describeCall.tools) != 0 {
				t.Errorf("describe step should have 0 tools, got %d", len(describeCall.tools))
			}
		})
	}
}

// TestStageLLMCall_VisionDescribeErrorFallback verifies that when the
// describe step fails (e.g. transient vision hiccup), the stage does
// NOT fail the whole turn. Instead it logs the error, drops the image,
// and continues as a text-only turn so the user still gets a reply —
// with a note that the image could not be read.
func TestStageLLMCall_VisionDescribeErrorFallback(t *testing.T) {
	describeErr := errors.New("vision model timeout")
	mock := &mockStageChatter{
		describeErr: describeErr,
		toolResp: &llm.Message{
			Role:    "assistant",
			Content: "I'll need you to describe the image then.",
		},
	}
	turn := &Turn{
		User:     &config.UserConfig{Name: "parent", Role: "parent"},
		Tools:    []Tool{{Name: "builtin__echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}},
		Messages: []Message{{Role: "system", Content: "sys"}, imageMsg("add this to my inventory")},
	}
	stage := NewStageLLMCall(LLMCallDeps{
		ClientFactory: func(*Turn) llm.Chatter { return mock },
		Temperature:   0.7,
		MaxTokens:     512,
	})
	err := stage(context.Background(), turn)
	if err != nil {
		t.Fatalf("stage should not fail when describe step errors: %v", err)
	}
	if len(mock.withToolsCalls) != 2 {
		t.Fatalf("expected 2 ChatWithTools calls (describe + tool), got %d", len(mock.withToolsCalls))
	}
	// The tool-step messages must have no image.
	second := mock.withToolsCalls[1]
	if hasImageParts(second.msgs) {
		t.Error("tool-step messages must NOT contain the image after describe failure")
	}
	// The tool-step messages must contain the updated fallback note that's more honest.
	found := false
	for _, m := range second.msgs {
		if strings.Contains(m.Content, "couldn't process the image") &&
			strings.Contains(m.Content, "vision system is not configured or the describe step failed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("tool-step messages must contain the updated honest fallback note")
	}
	// The stage must not have returned an error.
	if turn.Output == "" {
		t.Error("turn.Output should not be empty (model should still respond)")
	}
}

// TestStageLLMCall_HonestVisionError provides a specific test for the
// fix to issue #282 - ensuring the error message is more honest about
// the actual reason for not processing the image.
func TestStageLLMCall_HonestVisionError(t *testing.T) {
	describeErr := errors.New("vision model timeout")
	mock := &mockStageChatter{
		describeErr: describeErr,
		toolResp: &llm.Message{
			Role:    "assistant",
			Content: "I'll need you to describe the image then.",
		},
	}
	turn := &Turn{
		User:     &config.UserConfig{Name: "parent", Role: "parent"},
		Tools:    []Tool{{Name: "builtin__echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}},
		Messages: []Message{{Role: "system", Content: "sys"}, imageMsg("add this to my inventory")},
	}
	stage := NewStageLLMCall(LLMCallDeps{
		ClientFactory: func(*Turn) llm.Chatter { return mock },
		Temperature:   0.7,
		MaxTokens:     512,
	})
	err := stage(context.Background(), turn)
	if err != nil {
		t.Fatalf("stage should not fail when describe step errors: %v", err)
	}

	// Verify the error message is now more honest about the actual cause
	// rather than just saying "I can't see images"
	second := mock.withToolsCalls[1]
	found := false
	for _, m := range second.msgs {
		if strings.Contains(m.Content, "couldn't process the image") &&
			strings.Contains(m.Content, "vision system is not configured or the describe step failed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("tool-step messages must contain the honest fallback note about vision system not configured or describe step failed")
	}
}

func TestHasImageParts(t *testing.T) {
	tests := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{name: "nil messages", msgs: nil, want: false},
		{name: "text only", msgs: []llm.Message{{Role: "user", Content: "hi"}}, want: false},
		{name: "text parts only", msgs: []llm.Message{{Role: "user", ContentParts: []any{map[string]any{"type": "text", "text": "hi"}}}}, want: false},
		{name: "image present", msgs: []llm.Message{{Role: "user", ContentParts: []any{map[string]any{"type": "text", "text": "hi"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,x"}}}}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasImageParts(tt.msgs); got != tt.want {
				t.Errorf("hasImageParts = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithImageDescription(t *testing.T) {
	original := []llm.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", ContentParts: []any{
			map[string]any{"type": "text", "text": "add this to inventory"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,x"}},
		}},
	}
	desc := "Orange square"
	result := withImageDescription(original, desc)

	// System message unchanged.
	if result[0].Content != "You are helpful." {
		t.Errorf("system content = %q, want %q", result[0].Content, "You are helpful.")
	}
	if result[0].ContentParts != nil {
		t.Error("system message should have nil ContentParts")
	}

	// User message: image replaced, original text + description preserved.
	if result[1].ContentParts != nil {
		t.Errorf("user ContentParts should be nil, got %v", result[1].ContentParts)
	}
	if !strings.Contains(result[1].Content, "add this to inventory") {
		t.Errorf("user content should contain original text: %q", result[1].Content)
	}
	if !strings.Contains(result[1].Content, "Orange square") {
		t.Errorf("user content should contain description: %q", result[1].Content)
	}
	if strings.Contains(result[1].Content, "data:image") {
		t.Error("user content should NOT contain image data")
	}
}
