package agent

import (
	"regexp"
	"strings"
)

// Pre-compiled regexes for stripping XML reasoning wrappers from LLM output.
// (?s) makes . match newlines so multi-line tags are handled in one pass.
var (
	reThinking       = regexp.MustCompile(`(?s)(?i)<thinking[^>]*>.*?</thinking[^>]*>`)
	reFinal          = regexp.MustCompile(`(?s)(?i)<final[^>]*>(.*?)</final[^>]*>`)
	reToolCallSpill  = regexp.MustCompile(`(?s)<tool_call>.*?</tool_call>`)
	reFunctionBlock  = regexp.MustCompile(`(?s)(?i)<function[^>]*>.*?</function[^>]*>`)
	reParameterBlock = regexp.MustCompile(`(?s)(?i)<parameter[^>]*>.*?</parameter[^>]*>`)
	reStrayThinking  = regexp.MustCompile(`(?i)</?thinking[^>]*>`)
	reStrayFunction  = regexp.MustCompile(`(?i)</?function[^>]*>`)
	reStrayParameter = regexp.MustCompile(`(?i)</?parameter[^>]*>`)
	reStrayFinal     = regexp.MustCompile(`(?i)</?final[^>]*>`)
	// selfClosing* match self-closing forms (<tag/>, <tag attr="x"/>, <tag />)
	// with word boundaries so prefixes like <thinkingx/> don't trip them.
	// These are stripped before the block regexes — self-closing tags have
	// no body and no closing tag, so they bypass the well-formed block matcher.
	reSelfClosingThinking  = regexp.MustCompile(`(?i)<thinking\b[^>]*/>`)
	reSelfClosingFunction  = regexp.MustCompile(`(?i)<function\b[^>]*/>`)
	reSelfClosingParameter = regexp.MustCompile(`(?i)<parameter\b[^>]*/>`)
	reSelfClosingFinal     = regexp.MustCompile(`(?i)<final\b[^>]*/>`)
	reBlankLines           = regexp.MustCompile(`\n{3,}`)
)

// sanitizeModelResponse strips XML reasoning wrappers from LLM output
// before it reaches user-facing gateways.
//
// Strips:
//   - <think>...</think> and <thinking>...</thinking> blocks (chain-of-thought)
//   - <final>...</final> wrapper — keeps the inner content
//   - Any remaining <tool_call>...</tool_call> blocks not handled by salvageInlineToolCalls()
//   - <function name="...">...</function> blocks (defensive strip)
//
// Returns the trimmed result. Input with no matching tags passes through unchanged.
func sanitizeModelResponse(input string) string {
	result := input

	// Strip self-closing tags first (<tag/>, <tag attr="x"/>, <tag />). They
	// carry no body and have no closing tag, so the well-formed block regexes
	// below would never match them.
	result = reSelfClosingThinking.ReplaceAllString(result, "")
	result = reSelfClosingFunction.ReplaceAllString(result, "")
	result = reSelfClosingParameter.ReplaceAllString(result, "")
	result = reSelfClosingFinal.ReplaceAllString(result, "")

	// Remove thinking blocks (well-formed pairs, then any leftover stray tags).
	result = stripBlock(result, reThinking, reStrayThinking)
	// Remove function blocks.
	result = stripBlock(result, reFunctionBlock, reStrayFunction)
	// Remove parameter blocks.
	result = stripBlock(result, reParameterBlock, reStrayParameter)
	// Remove tool-call spill blocks.
	result = reToolCallSpill.ReplaceAllString(result, "")
	// Unwrap <final>...</final>, then strip any stray final tags.
	result = unwrapFinalBlocks(result)
	result = reStrayFinal.ReplaceAllString(result, "")

	// Whitespace cleanup: trim and collapse 3+ newlines to 2.
	result = strings.TrimSpace(result)
	result = reBlankLines.ReplaceAllString(result, "\n\n")

	return result
}

// stripBlock repeatedly removes well-formed matches of `block`, then strips
// any remaining stray opening/closing tags matched by `stray`.
func stripBlock(s string, block, stray *regexp.Regexp) string {
	for {
		loc := block.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[:loc[0]] + s[loc[1]:]
	}
	return stray.ReplaceAllString(s, "")
}

// unwrapFinalBlocks repeatedly replaces outermost <final>...</final> pairs
// with their inner content. Looping handles nested <final> tags: after each
// substitution a new outermost pair becomes available, until none remain.
func unwrapFinalBlocks(s string) string {
	for {
		loc := reFinal.FindStringIndex(s)
		if loc == nil {
			break
		}
		sub := reFinal.FindStringSubmatch(s)
		if len(sub) < 2 {
			return s
		}
		s = s[:loc[0]] + sub[1] + s[loc[1]:]
	}
	return s
}
