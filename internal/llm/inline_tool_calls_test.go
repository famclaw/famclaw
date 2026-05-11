package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestSalvageInlineToolCalls(t *testing.T) {
	tests := []struct {
		name        string
		in          *Message
		wantName    string
		wantArgs    map[string]any
		wantContent string
		wantCalls   int
	}{
		{
			name: "trailing-block-after-prose",
			in: &Message{
				Role:    "assistant",
				Content: "Here are some tire options...\n\n<tool_call>\n<function=web_fetch>\n<parameter=url>\nhttp://localhost:8888/search?q=tires&format=json\n</parameter>\n</function>\n</tool_call>",
			},
			wantName:    "web_fetch",
			wantArgs:    map[string]any{"url": "http://localhost:8888/search?q=tires&format=json"},
			wantContent: "Here are some tire options...",
			wantCalls:   1,
		},
		{
			name: "leading-block-no-prose",
			in: &Message{
				Role:    "assistant",
				Content: "<tool_call>\n<function=spawn_agent>\n<parameter=prompt>\nfind beaches\n</parameter>\n</function>\n</tool_call>",
			},
			wantName:    "spawn_agent",
			wantArgs:    map[string]any{"prompt": "find beaches"},
			wantContent: "",
			wantCalls:   1,
		},
		{
			name: "no-block-at-all",
			in: &Message{
				Role:    "assistant",
				Content: "Plain assistant text with no tool call.",
			},
			wantContent: "Plain assistant text with no tool call.",
			wantCalls:   0,
		},
		{
			name: "already-has-structured-call-strips-leak",
			in: &Message{
				Role:      "assistant",
				Content:   "tail text\n<tool_call>\n<function=web_fetch>\n<parameter=url>\nhttp://x\n</parameter>\n</function>\n</tool_call>",
				ToolCalls: []ToolCall{{ID: "x", Function: ToolCallFunction{Name: "web_fetch"}}},
			},
			wantContent: "tail text",
			wantCalls:   1, // unchanged — pre-existing tool call kept, no duplicate
		},
		{
			name: "malformed-block-stripped-no-call",
			in: &Message{
				Role:    "assistant",
				Content: "hi\n<tool_call>\n  garbage with no function tag\n</tool_call>",
			},
			wantContent: "hi",
			wantCalls:   0,
		},
		{
			name: "multi-line-parameter-value",
			in: &Message{
				Role:    "assistant",
				Content: "<tool_call>\n<function=spawn_agent>\n<parameter=prompt>\nline one\nline two\nline three\n</parameter>\n</function>\n</tool_call>",
			},
			wantName:    "spawn_agent",
			wantArgs:    map[string]any{"prompt": "line one\nline two\nline three"},
			wantContent: "",
			wantCalls:   1,
		},
		{
			name:        "nil-content",
			in:          &Message{Role: "assistant"},
			wantContent: "",
			wantCalls:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			salvageInlineToolCalls(tc.in)
			if tc.in.Content != tc.wantContent {
				t.Errorf("content = %q, want %q", tc.in.Content, tc.wantContent)
			}
			if len(tc.in.ToolCalls) != tc.wantCalls {
				t.Fatalf("got %d tool_calls, want %d", len(tc.in.ToolCalls), tc.wantCalls)
			}
			if tc.wantName == "" {
				return
			}
			// Pick the salvaged call — last one, since pre-existing calls
			// (when present) are kept at the front.
			last := tc.in.ToolCalls[len(tc.in.ToolCalls)-1]
			if last.Function.Name != tc.wantName {
				t.Errorf("name = %q, want %q", last.Function.Name, tc.wantName)
			}
			if tc.wantArgs != nil && !reflect.DeepEqual(map[string]any(last.Function.Arguments), tc.wantArgs) {
				t.Errorf("args = %v, want %v", last.Function.Arguments, tc.wantArgs)
			}
			if !strings.HasPrefix(last.ID, "inline_") {
				t.Errorf("salvaged call ID %q should start with inline_", last.ID)
			}
		})
	}
}

func TestSalvageInlineToolCalls_JSONTypedParameter(t *testing.T) {
	// The inline format is plain text, but if the model emits a JSON
	// literal (a number, a bool, an array) we should keep its type so
	// downstream tools see correctly-typed arguments.
	msg := &Message{
		Role: "assistant",
		Content: "<tool_call>\n<function=set_thing>\n" +
			"<parameter=count>42</parameter>\n" +
			"<parameter=flag>true</parameter>\n" +
			"<parameter=tags>[\"a\",\"b\"]</parameter>\n" +
			"<parameter=note>plain text stays as string</parameter>\n" +
			"</function>\n</tool_call>",
	}
	salvageInlineToolCalls(msg)
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool_calls, want 1", len(msg.ToolCalls))
	}
	args := map[string]any(msg.ToolCalls[0].Function.Arguments)

	if got, want := args["count"], float64(42); got != want {
		t.Errorf("count = %v (%T), want %v (float64)", got, got, want)
	}
	if got, want := args["flag"], true; got != want {
		t.Errorf("flag = %v, want %v", got, want)
	}
	if tags, ok := args["tags"].([]any); !ok || len(tags) != 2 {
		t.Errorf("tags = %v, want []any of length 2", args["tags"])
	}
	if got, want := args["note"], "plain text stays as string"; got != want {
		t.Errorf("note = %v, want %v", got, want)
	}
}
