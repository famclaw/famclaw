package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMessage_MarshalJSON_Multimodal tests that Message correctly marshals when ContentParts is set.
func TestMessage_MarshalJSON_Multimodal(t *testing.T) {
	// Create a message with multimodal content (text + image)
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

	// Marshal the message
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// Unmarshal into a map to check the structure
	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	// Check that role is correct
	require.Equal(t, "user", result["role"])

	// Check that content is an array
	content, ok := result["content"].([]any)
	require.True(t, ok, "content should be an array")
	require.Len(t, content, 2, "should have 2 content parts")

	// Check first part (text)
	textPart, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", textPart["type"])
	require.Equal(t, "What's in this image?", textPart["text"])

	// Check second part (image)
	imagePart, ok := content[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image_url", imagePart["type"])
	imageURL, ok := imagePart["image_url"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/", imageURL["url"])
}

// TestMessage_MarshalJSON_BackwardCompatibility tests that Message maintains backward compatibility with string Content.
func TestMessage_MarshalJSON_BackwardCompatibility(t *testing.T) {
	// Create a message with traditional string content
	msg := Message{
		Role:    "user",
		Content: "Hello, how are you?",
	}

	// Marshal the message
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// Unmarshal into a map to check the structure
	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	// Check that role is correct
	require.Equal(t, "user", result["role"])

	// Check that content is a string
	content, ok := result["content"].(string)
	require.True(t, ok, "content should be a string")
	require.Equal(t, "Hello, how are you?", content)
}

// TestMessage_MarshalJSON_EmptyContentParts_FallsBackToString tests that when ContentParts is empty nil or empty slice, it falls back to Content string.
func TestMessage_MarshalJSON_EmptyContentParts_FallsBackToString(t *testing.T) {
	// Test with nil ContentParts
	msg := Message{
		Role:    "user",
		Content: "Hello, how are you?",
		ContentParts: nil,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	content, ok := result["content"].(string)
	require.True(t, ok)
	require.Equal(t, "Hello, how are you?", content)

	// Test with empty ContentParts slice
	msg.ContentParts = []any{}
	data, err = json.Marshal(msg)
	require.NoError(t, err)

	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	content, ok = result["content"].(string)
	require.True(t, ok)
	require.Equal(t, "Hello, how are you?", content)
}

// TestMessage_MarshalJSON_WithToolCalls tests that Message correctly marshals multimodal content with tool calls.
func TestMessage_MarshalJSON_WithToolCalls(t *testing.T) {
	// Create a message with multimodal content and tool calls
	msg := Message{
		Role:    "assistant",
		ContentParts: []any{
			map[string]any{
				"type": "text",
				"text": "I can see a cat in the image.",
			},
		},
		ToolCalls: []ToolCall{
			{
				ID:      "call_123",
				Type:    "function",
				Function: ToolCallFunction{
					Name: "image_description",
					Arguments: map[string]any{
						"description": "A cute cat sitting on a windowsill",
					},
				},
			},
		},
	}

	// Marshal the message
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// Unmarshal into a map to check the structure
	var result map[string]any
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	// Check that role is correct
	require.Equal(t, "assistant", result["role"])

	// Check that content is an array
	content, ok := result["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	// Check that tool calls are present
	toolCalls, ok := result["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 1)

	// Check first tool call
	firstCall := toolCalls[0].(map[string]any)
	require.Equal(t, "call_123", firstCall["id"])
	require.Equal(t, "function", firstCall["type"])
	function := firstCall["function"].(map[string]any)
	require.Equal(t, "image_description", function["name"])
	arguments := function["arguments"].(map[string]any)
	require.Equal(t, "A cute cat sitting on a windowsill", arguments["description"])
}