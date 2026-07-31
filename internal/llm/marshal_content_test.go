package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMessage_MarshalJSON_EmptyContentAlwaysPresent is the headline regression
// test. llama-server rejects any non-assistant message that omits the "content"
// key ("All non-assistant messages must contain 'content'"). With the old
// `content,omitempty` tag a tool-role result with empty Content serialized to
// {"role":"tool","tool_call_id":"..."} — no content key — which made every
// conversation fail on llama.cpp-served models. Non-assistant roles must now
// emit "content":"" even when empty.
func TestMessage_MarshalJSON_EmptyContentAlwaysPresent(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		role string
	}{
		{
			name: "tool-role empty content emits content key",
			msg:  Message{Role: "tool", Content: "", ToolCallID: "call_1"},
			role: "tool",
		},
		{
			name: "user-role empty content emits content key",
			msg:  Message{Role: "user", Content: ""},
			role: "user",
		},
		{
			name: "system-role empty content emits content key",
			msg:  Message{Role: "system", Content: ""},
			role: "system",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			require.NoError(t, err)

			var result map[string]any
			require.NoError(t, json.Unmarshal(data, &result))

			require.Equal(t, tt.role, result["role"])

			content, ok := result["content"]
			require.Truef(t, ok, "content key must be present for role=%s; got %s", tt.role, string(data))
			require.Equalf(t, "", content, "content should be empty string for role=%s", tt.role)
		})
	}
}

// TestMessage_MarshalJSON_AssistantToolCallsOmitsContent ensures an assistant
// message carrying only tool_calls still omits the "content" key. Assistant
// messages legitimately have no content when they only issue tool calls, and
// llama-server permits this.
func TestMessage_MarshalJSON_AssistantToolCallsOmitsContent(t *testing.T) {
	msg := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: ToolCallFunction{
					Name:      "get_weather",
					Arguments: map[string]any{"location": "Tokyo"},
				},
			},
		},
		// Content intentionally empty.
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))

	require.Equal(t, "assistant", result["role"])
	_, ok := result["content"]
	require.Falsef(t, ok, "assistant message with only tool_calls should omit content; got %s", string(data))

	toolCalls, ok := result["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 1)
}

// TestMessage_MarshalJSON_NonEmptyContentUnchanged ensures real content is
// still serialized as a string on every role and is not accidentally dropped.
func TestMessage_MarshalJSON_NonEmptyContentUnchanged(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "user with content",
			msg:  Message{Role: "user", Content: "Hello!"},
			want: "Hello!",
		},
		{
			name: "assistant with content",
			msg:  Message{Role: "assistant", Content: "Hi there"},
			want: "Hi there",
		},
		{
			name: "tool result with content",
			msg:  Message{Role: "tool", Content: "22°C", ToolCallID: "call_1"},
			want: "22°C",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			require.NoError(t, err)

			var result map[string]any
			require.NoError(t, json.Unmarshal(data, &result))

			content, ok := result["content"].(string)
			require.True(t, ok, "content should be a string")
			require.Equal(t, tt.want, content)
		})
	}
}

// TestMessage_MarshalJSON_MultimodalContentUnaffected is a regression guard
// ensuring the multimodal ContentParts path still emits "content" as an array
// and is not touched by the empty-content fix.
func TestMessage_MarshalJSON_MultimodalContentUnaffected(t *testing.T) {
	msg := Message{
		Role: "user",
		ContentParts: []any{
			map[string]any{
				"type": "text",
				"text": "What's in this image?",
			},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/",
				},
			},
		},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))

	require.Equal(t, "user", result["role"])

	content, ok := result["content"].([]any)
	require.True(t, ok, "content should be an array for multimodal messages")
	require.Len(t, content, 2)

	textPart, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", textPart["type"])
	require.Equal(t, "What's in this image?", textPart["text"])
}
