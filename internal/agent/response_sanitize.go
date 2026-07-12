package agent

import (
	"regexp"
	"strings"
)

// Pre-compiled regexes for stripping XML reasoning wrappers from LLM output.
// (?s) makes . match newlines so multi-line tags are handled in one pass.
var (
	reThinking      = regexp.MustCompile(`(?s)(?i)<thinking[^>]*>.*?</thinking[^>]*>`)
	reFinal         = regexp.MustCompile(`(?s)(?i)<final[^>]*>(.*?)</final[^>]*>`)
	reToolCallSpill = regexp.MustCompile(`(?s)<tool_call>(.*?)<tool_call>`)
	reFunctionBlock = regexp.MustCompile(`(?s)(?i)<function[^>]*>.*?</function[^>]*>`)
	reParameterBlock = regexp.MustCompile(`(?s)(?i)<parameter[^>]*>.*?</parameter[^>]*>`)
	reStrayThinking = regexp.MustCompile(`(?i)</?thinking[^>]*>`)
	reStrayFunction = regexp.MustCompile(`(?i)</?function[^>]*>`)
	reStrayParameter = regexp.MustCompile(`(?i)</?parameter[^>]*>`)
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

	// Remove thinking blocks (case-insensitive)
	result = removeThinkingBlocks(result)

	// Remove function blocks
	result = removeFunctionBlocks(result)

	// Remove parameter blocks
	result = removeParameterBlocks(result)

	// Remove tool call spill blocks
	result = removeToolCallSpill(result)

	// Unwrap final blocks
	result = unwrapFinalBlocks(result)

	// Apply whitespace rule: trim and collapse 3+ newlines to 2
	result = strings.TrimSpace(result)
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")

	return result
}

// removeThinkingBlocks removes all thinking blocks (case-insensitive) and any stray tags.
func removeThinkingBlocks(s string) string {
	// First, remove well-formed thinking blocks (non-greedy, dot matches newline)
	for {
		loc := reThinking.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[:loc[0]] + s[loc[1]:]
	}
	// Remove any remaining opening or closing thinking tags
	s = reStrayThinking.ReplaceAllString(s, "")
	return s
}

// removeFunctionBlocks removes all function blocks (case-insensitive) and any stray tags.
func removeFunctionBlocks(s string) string {
	for {
		loc := reFunctionBlock.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[:loc[0]] + s[loc[1]:]
	}
	s = reStrayFunction.ReplaceAllString(s, "")
	return s
}

// removeParameterBlocks removes all parameter blocks (case-insensitive) and any stray tags.
func removeParameterBlocks(s string) string {
	for {
		loc := reParameterBlock.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[:loc[0]] + s[loc[1]:]
	}
	s = reStrayParameter.ReplaceAllString(s, "")
	return s
}

// removeToolCallSpill removes  ... ? blocks.
func removeToolCallSpill(s string) string {
	return regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`).ReplaceAllString(s, "")
}

// unwrapFinalBlocks repeatedly unwraps final tags.
func unwrapFinalBlocks(s string) string {
	// Use a more robust approach to handle nested final tags
	for {
		// Find the first occurrence of a final tag
		loc := reFinal.FindStringIndex(s)
		if loc == nil {
			break
		}
		
		// Extract the inner content (group 1)
		submatch := reFinal.FindStringSubmatch(s)
		if len(submatch) >= 2 {
			inner := submatch[1]
			// Replace the entire final tag with just the inner content
			s = s[:loc[0]] + inner + s[loc[1]:]
		} else {
			// Should not happen, but break to avoid infinite loop
			break
		}
	}
	return s
}
