package agentcore

import (
	"context"
	"fmt"
	"log"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/famclaw/famclaw/internal/llm"
)

// splitMCPContent splits an MCP tool result's content blocks into joined
// text and base64 image payloads (returned as data: URIs). Before this,
// the tool loop JSON-stringified result.Content wholesale, so an image
// block arrived in the transcript as a raw base64 blob — invisible to the
// vision path, context-window-destroying, and copied into every audit
// copy of the transcript.
func splitMCPContent(content []mcpgo.Content) (text string, images []string) {
	var texts []string
	for _, c := range content {
		switch block := c.(type) {
		case mcpgo.TextContent:
			texts = append(texts, block.Text)
		case *mcpgo.TextContent:
			texts = append(texts, block.Text)
		case mcpgo.ImageContent:
			images = append(images, imageDataURI(block.MIMEType, block.Data))
		case *mcpgo.ImageContent:
			images = append(images, imageDataURI(block.MIMEType, block.Data))
		}
	}
	return strings.Join(texts, "\n"), images
}

// imageDataURI formats base64 payload + mime as an OpenAI-style image_url
// data URI. Falls back to image/png when the server omitted the mime type.
func imageDataURI(mimeType, base64Data string) string {
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64Data
}

// imagesToContentParts wraps data URIs as OpenAI-style image_url content
// parts for the describe request.
func imagesToContentParts(images []string) []any {
	parts := make([]any, 0, len(images))
	for _, uri := range images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": uri},
		})
	}
	return parts
}

// describeBatchImages runs the no-tools vision describe step over the
// images a batch of MCP tool results produced, reusing the same
// describeImageIfPresent mechanism as stage_llm_call (local multimodal
// models silently drop tool calls when an image and tools share a
// request, so the description is produced separately and only its text
// continues). A describe failure must not deny the user a reply: the
// image is dropped and an honest note is returned instead.
func describeBatchImages(ctx context.Context, client llm.Chatter, temperature float64, images []string) string {
	descMsgs := []llm.Message{{
		Role:         "user",
		Content:      "describe",
		ContentParts: imagesToContentParts(images),
	}}
	desc, err := describeImageIfPresent(ctx, client, descMsgs, temperature, 0)
	if err != nil {
		log.Printf("[agentcore][tool_loop] screenshot describe failed: %v - dropping image", err)
		return fmt.Sprintf("[vision] describe step failed (%v); screenshot dropped, not shown to you.", err)
	}
	if strings.TrimSpace(desc) == "" {
		return "[vision] describe step returned no description; screenshot dropped, not shown to you."
	}
	return "[vision description of tool screenshot(s)]: " + desc
}
