package agentcore

import (
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// TestSplitMCPContent is the regression test for the screenshot base64
// bug: an MCP tool result carrying an ImageContent block used to be
// JSON-stringified wholesale into the transcript. The splitter must keep
// text and images separate.
func TestSplitMCPContent(t *testing.T) {
	const b64 = "ZmFrZS1zY3JlZW4tYmFzZTY0" // "fake-screen-base64"
	cases := []struct {
		name         string
		content      []mcpgo.Content
		wantText     string
		wantImages   []string
	}{
		{
			name:        "text only",
			content:     []mcpgo.Content{mcpgo.NewTextContent("hello")},
			wantText:    "hello",
			wantImages:  nil,
		},
		{
			name: "text + image (android_screenshot result)",
			content: []mcpgo.Content{
				mcpgo.NewTextContent("screen captured"),
				mcpgo.NewImageContent(b64, "image/png"),
			},
			wantText:   "screen captured",
			wantImages: []string{"data:image/png;base64," + b64},
		},
		{
			name: "image without mime falls back to image/png",
			content: []mcpgo.Content{
				mcpgo.ImageContent{Type: "image", Data: b64},
			},
			wantText:   "",
			wantImages: []string{"data:image/png;base64," + b64},
		},
		{
			name: "pointer content blocks (wire-decoded form)",
			content: []mcpgo.Content{
				&mcpgo.TextContent{Type: "text", Text: "ptr text"},
				&mcpgo.ImageContent{Type: "image", Data: b64, MIMEType: "image/jpeg"},
			},
			wantText:   "ptr text",
			wantImages: []string{"data:image/jpeg;base64," + b64},
		},
		{
			name:    "empty content",
			content: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, images := splitMCPContent(tc.content)
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
			if len(images) != len(tc.wantImages) {
				t.Fatalf("images = %d, want %d", len(images), len(tc.wantImages))
			}
			for i, got := range images {
				if got != tc.wantImages[i] {
					t.Errorf("image[%d] = %q, want %q", i, got, tc.wantImages[i])
				}
				if strings.Contains(got, "\"") {
					t.Errorf("image data URI must not be JSON-quoted: %q", got)
				}
			}
		})
	}
}

func TestImagesToContentParts(t *testing.T) {
	parts := imagesToContentParts([]string{"data:image/png;base64,AAAA"})
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	pm, ok := parts[0].(map[string]any)
	if !ok || pm["type"] != "image_url" {
		t.Fatalf("part shape wrong: %#v", parts[0])
	}
	iu, ok := pm["image_url"].(map[string]any)
	if !ok || iu["url"] != "data:image/png;base64,AAAA" {
		t.Fatalf("image_url shape wrong: %#v", pm["image_url"])
	}
}
