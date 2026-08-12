package agentcore

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/famclaw/famclaw/internal/llm"
)

// LLMCallDeps holds dependencies for the LLM call stage.
type LLMCallDeps struct {
	ClientFactory func(turn *Turn) llm.Chatter // creates a client for this turn's LLM profile
	Temperature   float64
	MaxTokens     int
	OnToken       func(string) // streaming callback (can be nil)
}

// NewStageLLMCall returns a stage that calls the LLM with the turn's messages.
// If tools are available, uses non-streaming ChatWithTools.
// Otherwise, uses streaming Chat.
func NewStageLLMCall(deps LLMCallDeps) Stage {
	return func(ctx context.Context, turn *Turn) error {
		client := deps.ClientFactory(turn)
		if client == nil {
			return fmt.Errorf("LLM not configured — open the web UI to set up your AI backend")
		}

		// Convert turn messages to llm.Message
		llmMsgs := turnToLLMMessages(turn.Messages)

		if len(turn.Tools) > 0 {
			// Two-step vision+tools workaround: local multimodal models
			// (gemma-4-26b) silently drop tool calls when an image and tools
			// are sent in the same request. Instead, describe the image
			// without tools, then feed the description back as text into the
			// tool-enabled call. See data/fc-vision-plus-tools/report.md.
			if desc, err := describeImageIfPresent(ctx, client, llmMsgs, deps.Temperature, deps.MaxTokens); err != nil {
				// A transient vision hiccup must not deny the user a reply.
				// Log the error, drop the image, and continue as a text-only
				// turn — the fallback note tells the model the image was
				// unreadable so it can ask the user to describe it.
				log.Printf("[agentcore][stage_llm_call] vision describe failed: %v - dropping image, continuing as text-only", err)
				llmMsgs = withImageDescription(llmMsgs, visionDescribeFallback)
			} else if desc != "" {
				llmMsgs = withImageDescription(llmMsgs, desc)
			}

			// Non-streaming with tools
			toolDefs := toolsToLLMDefs(turn.Tools)
			msg, err := client.ChatWithTools(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, toolDefs)
			if err != nil {
				return fmt.Errorf("LLM error: %w", err)
			}
			turn.Output = msg.Content
			// Store tool calls for the tool loop stage
			if len(msg.ToolCalls) > 0 {
				turn.SetMeta("pending_tool_calls", msg.ToolCalls)
				turn.SetMeta("llm_messages", llmMsgs)
			}
			return nil
		}

		// Streaming without tools
		response, err := client.Chat(ctx, llmMsgs, deps.Temperature, deps.MaxTokens, deps.OnToken)
		if err != nil {
			return fmt.Errorf("LLM error: %w", err)
		}
		turn.Output = response
		turn.Streamed = deps.OnToken != nil
		return nil
	}
}

func turnToLLMMessages(msgs []Message) []llm.Message {
	result := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		result[i] = llm.Message{
			Role:         m.Role,
			Content:      m.Content,
			ContentParts: m.ContentParts,
		}
		if len(m.ToolCalls) > 0 {
			result[i].ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				result[i].ToolCalls[j] = llm.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: llm.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
	}
	return result
}

func toolsToLLMDefs(tools []Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		// Strip the "builtin__" prefix so the name advertised to the LLM
		// matches the capabilities prompt ("web_search", not
		// "builtin__web_search"). MCP names keep their
		// "mcp__<server>__<tool>" namespace for disambiguation.
		name := t.Name
		if bare, ok := strings.CutPrefix(name, "builtin__"); ok {
			name = bare
		}
		defs[i] = llm.ToolDef{
			Type: "function",
			Function: llm.ToolDefFunc{
				Name:        name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return defs
}

// visionDescMaxTokens is the token budget for the image-description step
// of the two-step vision+tools workaround. gemma-4-26b is a reasoning
// model that ships its final answer via reasoning_content; with a small
// budget the reasoning consumes the entire window and content comes back
// empty (indistinguishable from "vision is broken"). 1000 is the
// firstmate-measured safe floor (>=900). See
// data/fc-vision-plus-tools/report.md.
const visionDescMaxTokens = 1000

// visionDescInstruction replaces the user's text in the description step
// so the model describes what it sees rather than acting on the original
// intent. No tools are provided in this step, but the instruction keeps
// the response focused and factual.
const visionDescInstruction = "Describe the image above factually and concisely: the objects, their colors, any text, and their spatial arrangement. Do not act, do not call tools, do not speculate."

// visionDescribeFallback is the text substituted for the image when the
// describe step fails. Dropping the image content parts without a note
// would let the model answer as if no picture were attached; the note
// ensures the user knows the image was ignored and can describe it.
// This message is now more honest about why the image wasn't processed.
const visionDescribeFallback = "Note: I couldn't process the image you attached because the vision system is not configured or the describe step failed. Please describe what you'd like me to do with it."

// hasImageParts reports whether any message carries an image_url content
// part.
func hasImageParts(msgs []llm.Message) bool {
	for _, m := range msgs {
		for _, p := range m.ContentParts {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}

// describeImageIfPresent runs the image-description step when the message
// list contains images. It sends the image with NO tools (gemma-4-26b
// silently fails image+tools) and returns the model's description.
// When the message list has no images, it returns "" and makes no LLM
// call. The description step always uses a fixed budget of
// visionDescMaxTokens, independent of the caller's MaxTokens.
func describeImageIfPresent(ctx context.Context, client llm.Chatter, msgs []llm.Message, temp float64, maxTokens int) (string, error) {
	if !hasImageParts(msgs) {
		return "", nil
	}
	// Build the description request: replace each user message's text with
	// the description instruction while keeping the image parts.
	descMsgs := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		if m.Role != "user" || len(m.ContentParts) == 0 {
			descMsgs[i] = m
			continue
		}
		var images []any
		for _, p := range m.ContentParts {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
				images = append(images, p)
			}
		}
		if len(images) == 0 {
			descMsgs[i] = m
			continue
		}
		descMsgs[i] = llm.Message{
			Role: m.Role,
			ContentParts: append(
				[]any{map[string]any{"type": "text", "text": visionDescInstruction}},
				images...,
			),
		}
	}
	// Fixed budget, independent of the caller's MaxTokens: not a floor
	// (which would balloon spend to 4096 on a high-token caller) and not
	// a min() (which would starve below the ~900 the thinking model needs
	// to emit content). 1000 is the firstmate-measured safe floor per
	// data/fc-vision-plus-tools/report.md.
	_ = maxTokens // reserved for future caller-aware tuning; do not min() here
	descMaxTokens := visionDescMaxTokens
	msg, err := client.ChatWithTools(ctx, descMsgs, temp, descMaxTokens, nil)
	if err != nil {
		return "", fmt.Errorf("vision describe step: %w", err)
	}
	return msg.Content, nil
}

// withImageDescription replaces image_url content parts with the model's
// description text so the tool-enabled call can act on what the image
// showed without sending the image alongside tools (which gemma-4-26b
// silently mishandles). Messages without images are passed through
// unchanged.
func withImageDescription(msgs []llm.Message, desc string) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		if len(m.ContentParts) == 0 {
			out[i] = m
			continue
		}
		var hasImg bool
		var b strings.Builder
		if m.Content != "" {
			b.WriteString(m.Content)
		}
		for _, p := range m.ContentParts {
			if pm, ok := p.(map[string]any); ok {
				switch pm["type"] {
				case "image_url":
					hasImg = true
				case "text":
					if t, ok := pm["text"].(string); ok {
						b.WriteString(t)
						b.WriteString(" ")
					}
				}
			}
		}
		if !hasImg {
			out[i] = m
			continue
		}
		b.WriteString(desc)
		out[i] = llm.Message{
			Role:      m.Role,
			Content:   strings.TrimSpace(b.String()),
			ToolCalls: m.ToolCalls,
		}
	}
	return out
}
