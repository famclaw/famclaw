package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
)

// TestSplitMCPContent is the regression test for the screenshot base64
// bug: an MCP tool result carrying an ImageContent block used to be
// JSON-stringified wholesale into the transcript. The splitter must keep
// text and images separate.
func TestSplitMCPContent(t *testing.T) {
	const b64 = "ZmFrZS1zY3JlZW4tYmFzZTY0" // "fake-screen-base64"
	cases := []struct {
		name       string
		content    []mcpgo.Content
		wantText   string
		wantImages []string
	}{
		{
			name:       "text only",
			content:    []mcpgo.Content{mcpgo.NewTextContent("hello")},
			wantText:   "hello",
			wantImages: nil,
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

// leakChatter is a minimal llm.Chatter that returns a fixed sequence of
// responses to ChatWithTools and records, per call, the message slice and
// the number of tool definitions it was invoked with — so a test can tell
// the no-tools vision-describe call from the tool-loop follow-up call and
// assert which one carried the image.
type leakChatter struct {
	mu        sync.Mutex
	responses []llm.Message
	calls     []capturedCall
}

// capturedCall is one ChatWithTools invocation as seen by leakChatter.
type capturedCall struct {
	msgs  []llm.Message
	tools int
}

func (c *leakChatter) Chat(_ context.Context, _ []llm.Message, _ float64, _ int, _ func(string)) (string, error) {
	return "done", nil
}
func (c *leakChatter) ChatMessage(_ context.Context, _ []llm.Message, _ float64, _ int) (*llm.Message, error) {
	return &llm.Message{Role: "assistant", Content: "done"}, nil
}
func (c *leakChatter) ChatSync(_ context.Context, _ []llm.Message, _ float64, _ int) (string, error) {
	return "done", nil
}
func (c *leakChatter) ChatWithTools(_ context.Context, msgs []llm.Message, _ float64, _ int, tools []llm.ToolDef) (*llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := make([]llm.Message, len(msgs))
	copy(snap, msgs)
	c.calls = append(c.calls, capturedCall{msgs: snap, tools: len(tools)})
	if len(c.responses) == 0 {
		return &llm.Message{Role: "assistant", Content: "done"}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	return &resp, nil
}
func (c *leakChatter) Ping(context.Context) error { return nil }

// TestStageToolLoop_MCPImageNoLeak is the end-to-end proof of the
// "describe-then-text" variant (design §3.4). It drives the real tool loop
// against a real in-process MCP pool whose screenshot tool returns an
// ImageContent (the android-control shape) and asserts the no-leak
// invariant end to end:
//
//  1. The butler's vision model actually receives the MCP screenshot — the
//     base64 reaches exactly one LLM call, the no-tools describe step.
//  2. The base64 never appears in the tool-loop follow-up transcript
//     (the call that carries tool definitions) — only the text description
//     does.
//  3. The persisted ToolResult.Output holds only the short note, not the
//     base64 blob that would otherwise be copied into every audit copy.
func TestStageToolLoop_MCPImageNoLeak(t *testing.T) {
	const b64 = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpBREY=" // base64("ABC...Z")
	const wantDesc = "The screen shows the Settings app."

	// In-process MCP server standing in for android-control's screenshot.
	srv := mcpgoserver.NewMCPServer("android-control", "0.0.1")
	srv.AddTool(mcpgo.NewTool("screenshot",
		mcpgo.WithDescription("Capture the current screen."),
	), func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultImage("Screenshot: 42 bytes of PNG.", b64, "image/png"), nil
	})

	pool := mcp.NewPool("", false, false)
	if err := pool.RegisterInProcessServer("android-control", srv); err != nil {
		t.Fatalf("RegisterInProcessServer: %v", err)
	}

	chatter := &leakChatter{responses: []llm.Message{
		{Role: "assistant", Content: wantDesc}, // 1st call: vision describe step
		{Role: "assistant", Content: "done"},   // 2nd call: tool-loop follow-up
	}}

	turn := &Turn{
		User:  &config.UserConfig{Name: "tester", Role: "child"},
		Tools: []Tool{{Name: "screenshot"}},
	}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call_shot",
		Function: llm.ToolCallFunction{Name: "screenshot", Arguments: map[string]any{}},
	}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "take a screenshot of the device"}})

	stage := NewStageToolLoop(ToolLoopDeps{
		Pool:          pool,
		MaxIterations: 10,
		ClientFactory: func(*Turn) llm.Chatter { return chatter },
	})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	chatter.mu.Lock()
	calls := append([]capturedCall(nil), chatter.calls...)
	chatter.mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 LLM calls (describe + follow-up), got %d", len(calls))
	}

	// 1) The vision model received the MCP screenshot: the describe call
	//    (no tool definitions) carries the image part + the instruction.
	desc := calls[0]
	if desc.tools != 0 {
		t.Fatalf("describe call must send no tool definitions, got %d", desc.tools)
	}
	sawImage, sawInstruction := false, false
	for _, m := range desc.msgs {
		for _, p := range m.ContentParts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "image_url":
				if iu, ok := pm["image_url"].(map[string]any); ok && iu["url"] == "data:image/png;base64,"+b64 {
					sawImage = true
				}
			case "text":
				if txt, _ := pm["text"].(string); strings.Contains(txt, "Describe the image") {
					sawInstruction = true
				}
			}
		}
	}
	if !sawImage {
		t.Errorf("describe call did not carry the MCP screenshot image part")
	}
	if !sawInstruction {
		t.Errorf("describe call did not carry the vision describe instruction")
	}

	// 2) No leak: the base64 must not appear in any message sent to the
	//    tool-loop follow-up (the call that carries tool definitions).
	followUp := calls[1]
	if followUp.tools == 0 {
		t.Fatalf("follow-up call must carry tool definitions, got 0")
	}
	for i, m := range followUp.msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal follow-up msg %d: %v", i, err)
		}
		if strings.Contains(string(raw), b64) {
			t.Errorf("base64 image leaked into follow-up transcript msg %d (role=%s)", i, m.Role)
		}
	}

	// 3) Text transcript: the ToolResult holds only the note, and the text
	//    description reaches the follow-up call.
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Error != nil {
		t.Fatalf("expected 1 successful tool result, got %+v", turn.ToolCalls)
	}
	out := turn.ToolCalls[0].Output
	if strings.Contains(out, b64) {
		t.Errorf("base64 image leaked into ToolResult.Output: %q", out)
	}
	if !strings.Contains(out, "[screenshot returned by screenshot") {
		t.Errorf("ToolResult.Output missing the screenshot note: %q", out)
	}
	descSeen := false
	for _, m := range followUp.msgs {
		if m.Role == "user" && strings.Contains(m.Content, "[vision description of tool screenshot(s)]: "+wantDesc) {
			descSeen = true
		}
	}
	if !descSeen {
		t.Errorf("vision description did not reach the follow-up call as text")
	}
	if turn.Output != "done" {
		t.Errorf("turn.Output = %q, want %q", turn.Output, "done")
	}
}
