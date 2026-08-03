package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strings"
)

// Nemotron-3-Nano (and other models trained on the same chat template)
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

	hasLlamaBlock := strings.Contains(msg.Content, "<tool_call>")
	hasGemmaBlock := reGemmaToolCall.FindString(msg.Content) != ""
	if !hasLlamaBlock && !hasGemmaBlock {
		return
	}

	if hasLlamaBlock {
		if len(msg.ToolCalls) > 0 {
			// Upstream parser already lifted at least one call. Just sanitize
			// any stray inline blocks so they don't leak as visible XML — but
			// don't double-execute by parsing them as new calls.
			msg.Content = stripToolCallBlocks(msg.Content)
		} else {
			matches := reToolCall.FindAllStringSubmatchIndex(msg.Content, -1)
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
	}

	if hasGemmaBlock {
		salvageGemmaToolCalls(msg)
	}
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

// --- Gemma native format ( <|tool_call_begin|>...<|tool_call_end|> ) ---
//
// LiteLLM (and some Ollama/LiteLLM gateway configs) fail to promote
// Gemma native tool_call_begin> call:NAME {args} <|tool_call_end|> blocks
// into the structured tool_calls[] array. When that happens the raw
// tokens leak into the assistant visible content. These functions
// rescue the real tool calls and strip the tokens.
//
// Observed closing-tag variants in the wild (from the fc-convo-audit-scout
// report, 2026-07-12): <|tool_call_end|>, <|tool_call|>, and
// <tool_call|> — we match all of them.

// reGemmaToolCall matches a single Gemma tool-call block.
// Groups:
//
//	1 = function name
//	2 = raw argument body (inside { ... })
//
// The args body uses a non-greedy (.+?) capture so that nested
// objects (e.g. {"filter":{"a":1}}) are not truncated at the first
// closing brace. The closing brace is anchored by the required
// trailing closing-tag variant.
var reGemmaToolCall = regexp.MustCompile(`(?s)(?:<\|tool_call_begin\|>|<\|tool_call>)call:(\w+)\s*\{(.+?)\}\s*(?:<\|tool_call_end\|>|</\|tool_call\|>|<\|tool_call\|>|<tool_call\|>)`)

// reGemmaArg extracts a single key:<|"|>value<|"|> pair from the
// argument body. The <|"|> token is Gemma native string delimiter.
var reGemmaArg = regexp.MustCompile(`(\w+):<\|"\|>(.*?)<\|"\|>`)

// salvageGemmaToolCalls parses Gemma <|tool_call_begin|> blocks in
// msg.Content, promotes them to ToolCall entries, and strips the raw
// tokens so they never reach the user. No-op when no Gemma blocks
// are present.
func salvageGemmaToolCalls(msg *Message) {
	matches := reGemmaToolCall.FindAllStringSubmatchIndex(msg.Content, -1)
	if len(matches) == 0 {
		return
	}

	var calls []ToolCall
	for _, m := range matches {
		name := msg.Content[m[2]:m[3]]
		argBody := msg.Content[m[4]:m[5]]
		call, ok := parseGemmaToolCallBody(name, argBody)
		if ok {
			calls = append(calls, call)
		}
	}

	msg.Content = stripGemmaToolCallBlocks(msg.Content)
	msg.ToolCalls = append(msg.ToolCalls, calls...)
}

// parseGemmaToolCallBody builds a ToolCall from a Gemma function name
// and its raw argument body. The body may use <|"|> delimited
// key:value pairs, or may already be JSON.
func parseGemmaToolCallBody(name string, argBody string) (ToolCall, bool) {
	if name == "" {
		return ToolCall{}, false
	}
	args := parseGemmaArgs(argBody)
	encoded, err := json.Marshal(args)
	if err != nil {
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

// parseGemmaArgs parses a Gemma argument body. It first tries JSON,
// then the native <|"|> delimited format.
func parseGemmaArgs(argBody string) map[string]any {
	argBody = strings.TrimSpace(argBody)
	if argBody == "" {
		return map[string]any{}
	}

	// Try JSON first — handles the standard Gemma format where
	// arguments are a JSON object between <|tool_split|> and <|tool_call_end|>.
	var jsonArgs map[string]any
	if err := json.Unmarshal([]byte(argBody), &jsonArgs); err == nil {
		return jsonArgs
	}

	// Native <|"|> delimited key:value pairs.
	args := map[string]any{}
	seen := map[string]bool{}
	for _, p := range reGemmaArg.FindAllStringSubmatch(argBody, -1) {
		key := strings.TrimSpace(p[1])
		val := strings.TrimSpace(p[2])
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		var typed any
		if err := json.Unmarshal([]byte(val), &typed); err == nil {
			args[key] = typed
		} else {
			args[key] = val
		}
	}
	if len(args) > 0 {
		return args
	}

	// Last resort: treat the whole body as a single string value
	// under a generic key. This preserves the content for debugging
	// even when we can't parse the structure.
	return map[string]any{"raw": argBody}
}

// stripGemmaToolCallBlocks removes all <|tool_call_begin|>...closing
// blocks and collapses surrounding whitespace.
func stripGemmaToolCallBlocks(content string) string {
	stripped := reGemmaToolCall.ReplaceAllString(content, "")
	// After removing recognized tool-call blocks, any remaining <|...|>
	// token is an unrecognized control token. Log it at warn level so a
	// new Gemma format is diagnosable instead of silently deleted — the
	// family would otherwise get an empty or truncated reply with nothing
	// in the logs.
	for _, tok := range reControlToken.FindAllString(stripped, -1) {
		log.Printf("[llm] stripGemmaToolCallBlocks: unrecognized control token stripped: %q", tok)
	}
	stripped = reControlToken.ReplaceAllString(stripped, "")
	stripped = regexp.MustCompile(`\n{3,}`).ReplaceAllString(stripped, "\n\n")
	return strings.TrimSpace(stripped)
}

// stripControlTokens is the belt-and-braces final pass: it removes
// every Gemma control token — <|...|>, <|/|...|>, and bare <token|>
// variants — from a string. This guarantees that no model-internal
// control token can ever leak into user-visible content, even if a
// new, unanticipated token format appears. It runs AFTER format-specific
// parsing (salvageInlineToolCalls, salvageGemmaToolCalls) so that
// already-extracted tool calls are never affected.
//
// NOTE: this function does NOT TrimSpace. Callers that need trimming
// (mergeReasoning, chatFull) call strings.TrimSpace themselves. The
// streaming path uses this function on individual tokens where
// trimming would incorrectly drop space tokens.
var reControlToken = regexp.MustCompile(`<[^>]*\|[^>]*>`)

func stripControlTokens(content string) string {
	return reControlToken.ReplaceAllString(content, "")
}
