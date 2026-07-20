package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// A model like a local reasoning model (and other models trained on the same chat template)
// is instructed to emit tool calls in this XML-style form:
//
//	<tool_call>
//	<function=NAME>
//	<parameter=KEY>
//	VALUE
//	</parameter>
//	</function>
//	</tool_call>
//
// llama-server's tool-call parser only promotes this block to the
// structured tool_calls[] response field when the block appears at the
// START of the model's output. The trained spec ("provide reasoning
// BEFORE the function call, but NOT after") agrees, but small models
// frequently violate it — emitting prose first and the tool_call block
// after — and the trailing block then leaks through to the user as
// visible XML.
//
// salvageInlineToolCalls runs as a post-decode rescue: when an assistant
// response has empty tool_calls[] AND its content contains a recognisable
// <tool_call> block, parse the block, promote it to a ToolCall, and strip
// it from content. Malformed blocks are stripped silently rather than
// left in user-visible text — the tool loop will just see no tool calls
// and the model gets a chance to retry on the next turn.

var (
	reToolCall  = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	reFunction  = regexp.MustCompile(`(?s)<function=([^>\s]+)>(.*?)</function>`)
	reParameter = regexp.MustCompile(`(?s)<parameter=([^>\s]+)>(.*?)</parameter>`)
)

// salvageInlineToolCalls inspects msg.Content for <tool_call> XML blocks
// that should have been lifted into msg.ToolCalls by upstream parsing.
// When found and parseable, the calls are appended to msg.ToolCalls and
// stripped from msg.Content. No-op when msg already has tool_calls or
// when content has no recognisable block.
func salvageInlineToolCalls(msg *Message) {
	if msg == nil || msg.Content == "" {
		return
	}
	if !strings.Contains(msg.Content, "<tool_call>") {
		return
	}
	if len(msg.ToolCalls) > 0 {
		// Upstream parser already lifted at least one call. Just sanitize
		// any stray inline blocks so they don't leak as visible XML — but
		// don't double-execute by parsing them as new calls.
		msg.Content = stripToolCallBlocks(msg.Content)
		return
	}

	matches := reToolCall.FindAllStringSubmatchIndex(msg.Content, -1)
	if len(matches) == 0 {
		return
	}

	var calls []ToolCall
	for _, m := range matches {
		body := msg.Content[m[2]:m[3]]
		call, ok := parseInlineToolCallBody(body)
		if ok {
			calls = append(calls, call)
		}
	}

	msg.Content = stripToolCallBlocks(msg.Content)
	msg.ToolCalls = append(msg.ToolCalls, calls...)
}

// stripToolCallBlocks removes <tool_call>...</tool_call> blocks and the
// whitespace around them so the remaining content reads cleanly.
func stripToolCallBlocks(content string) string {
	stripped := reToolCall.ReplaceAllString(content, "")
	// Collapse runs of blank lines the stripped block may have left.
	stripped = regexp.MustCompile(`\n{3,}`).ReplaceAllString(stripped, "\n\n")
	return strings.TrimSpace(stripped)
}

// parseInlineToolCallBody extracts a single <function=NAME>...</function>
// from the body of a <tool_call> block and turns its <parameter=K>V</parameter>
// children into a ToolCall with JSON-encoded arguments. Returns ok=false
// when the body is missing the function tag or the function name is empty.
func parseInlineToolCallBody(body string) (ToolCall, bool) {
	fn := reFunction.FindStringSubmatch(body)
	if len(fn) < 3 {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(fn[1])
	if name == "" {
		return ToolCall{}, false
	}

	args := map[string]any{}
	for _, p := range reParameter.FindAllStringSubmatch(fn[2], -1) {
		key := strings.TrimSpace(p[1])
		val := strings.TrimSpace(p[2])
		if key == "" {
			continue
		}
		// Parameters are typed as JSON in the OpenAI tool schema but the
		// inline format emits them as plain text. Try to parse JSON first
		// (so numbers/booleans/objects round-trip correctly), fall back to
		// a string value.
		var typed any
		if err := json.Unmarshal([]byte(val), &typed); err == nil {
			args[key] = typed
		} else {
			args[key] = val
		}
	}

	encoded, err := json.Marshal(args)
	if err != nil {
		// Should be unreachable — map[string]any with primitive values
		// always marshals — but stay defensive rather than crash the turn.
		return ToolCall{}, false
	}

	return ToolCall{
		ID:   newInlineToolCallID(),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: mustUnmarshalArgs(encoded),
		},
	}, true
}

// mustUnmarshalArgs round-trips through ToolCallArguments to keep the
// downstream contract identical to upstream-parsed tool calls (string
// values stay strings, JSON-encoded structures decode as nested maps).
func mustUnmarshalArgs(data []byte) ToolCallArguments {
	var args ToolCallArguments
	// JSON we just produced is well-formed.
	_ = json.Unmarshal(data, &args)
	if args == nil {
		args = ToolCallArguments{}
	}
	return args
}

// newInlineToolCallID returns a synthetic, opaque ID for a salvaged call.
// Tool loops require an ID to correlate calls with their results;
// upstream-parsed calls get one from the server but inline ones don't.
// Prefix `inline_` makes the provenance visible in logs and debugging.
func newInlineToolCallID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "inline_fallback"
	}
	return "inline_" + hex.EncodeToString(b)
}
