package gateway

import (
	"regexp"
	"strings"
)

// brTagRE matches <br> tags (case-insensitive, optional slash)
var brTagRE = regexp.MustCompile(`(?i)<br\s*/?>`)

// markdownTableBlockRE matches one or more contiguous lines starting with |, including separator rows
var markdownTableBlockRE = regexp.MustCompile(`(?m)(^\s*\|.*\|\s*$\n?)+`)

// blankCollapseRE collapses 3+ newlines into exactly 2
var blankCollapseRE = regexp.MustCompile(`\n{3,}`)

// NormalizeReplyForChatGateway cleans a model's reply for Discord/Telegram delivery.
// It preserves code blocks verbatim, converts markdown tables to bullet lists,
// replaces <br> tags with ' / ', collapses excessive newlines, and trims.
func NormalizeReplyForChatGateway(s string) string {
	// Split the input into parts: code blocks and prose
	parts := splitOnFences(s)

	var result strings.Builder

	for _, part := range parts {
		if part.isCode {
			// Preserve code blocks verbatim
			result.WriteString(part.text)
		} else {
			// Process prose: convert tables, strip <br>, collapse newlines
			// First, convert markdown tables to bullet lists
			processed := convertMarkdownTables(part.text)
			// Then, strip remaining <br> tags in prose
			processed = brTagRE.ReplaceAllString(processed, " ")
			// Collapse 3+ newlines to exactly 2
			processed = blankCollapseRE.ReplaceAllString(processed, "\n\n")
			// Trim final whitespace
			result.WriteString(strings.TrimSpace(processed))
		}
	}

	return result.String()
}

// splitOnFences splits a string on triple-backtick fences.
// It returns a slice of fencePart, where isCode is true for code blocks.
// If an opening fence has no closing one, the rest of the string is treated as prose.
func splitOnFences(s string) []fencePart {
	var parts []fencePart
	start := 0

	for {
		// Find the next opening fence
		openIdx := strings.Index(s[start:], "```")
		if openIdx == -1 {
			// No more fences: add remaining text as prose
			if start < len(s) {
				parts = append(parts, fencePart{text: s[start:], isCode: false})
			}
			break
		}
		openIdx += start

		// Extract the text before the fence
		if openIdx > start {
			parts = append(parts, fencePart{text: s[start:openIdx], isCode: false})
		}

		// Find the closing fence
		closeIdx := strings.Index(s[openIdx+3:], "```")
		if closeIdx == -1 {
			// No closing fence: treat everything after as prose
			parts = append(parts, fencePart{text: s[openIdx+3:], isCode: false})
			break
		}
		closeIdx += openIdx + 3

		// Extract the code block content
		codeContent := s[openIdx+3:closeIdx]
		parts = append(parts, fencePart{text: codeContent, isCode: true})

		// Move start past the closing fence
		start = closeIdx + 3
	}

	return parts
}

// convertMarkdownTables converts markdown tables to bullet lists.
// It uses the table block regex to find table sections.
func convertMarkdownTables(s string) string {
	return markdownTableBlockRE.ReplaceAllStringFunc(s, func(block string) string {
		var headers []string
		var rows [][]string
		var headerSet bool

		// Split the block into lines
		lines := strings.Split(block, "\n")

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "|") {
				continue
			}

			// Split by | and remove empty first/last
			cells := strings.Split(line, "|")
			cells = cells[1 : len(cells)-1]

			// Skip separator rows (contain ---)
			if strings.Contains(line, "---") {
				continue
			}

			// First non-separator line is the header
			if !headerSet {
				for _, cell := range cells {
					headers = append(headers, stripMarkers(strings.TrimSpace(cell)))
				}
				headerSet = true
				continue
			}

			// Process data rows
			var row []string
			for _, cell := range cells {
				cleaned := brTagRE.ReplaceAllString(strings.TrimSpace(cell), " / ")
				row = append(row, cleaned)
			}
			rows = append(rows, row)
		}

		// Build the bullet list
		var bulletList strings.Builder
		for _, row := range rows {
			var parts []string
			for i, cell := range row {
				if i < len(headers) {
					parts = append(parts, "**"+headers[i]+": " + cell)
				} else {
					parts = append(parts, cell)
				}
			}
			bulletList.WriteString("- " + strings.Join(parts, "  •  ") + "\n")
		}

		return bulletList.String()
	})
}

// stripMarkers removes outer **, __, *, or _ from a string.
func stripMarkers(s string) string {
	// Remove outer ** or __
	if len(s) >= 4 && s[0] == '*' && s[1] == '*' && s[len(s)-2] == '*' && s[len(s)-1] == '*' {
		return s[2 : len(s)-2]
	}
	if len(s) >= 4 && s[0] == '_' && s[1] == '_' && s[len(s)-2] == '_' && s[len(s)-1] == '_' {
		return s[2 : len(s)-2]
	}

	// Remove outer * or _
	if len(s) >= 2 && (s[0] == '*' || s[0] == '_') && (s[len(s)-1] == '*' || s[len(s)-1] == '_') {
		return s[1 : len(s)-1]
	}

	return s
}

type fencePart struct {
	text   string
	isCode bool
}