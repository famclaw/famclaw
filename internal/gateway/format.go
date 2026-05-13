package gateway

import (
	"regexp"
	"strings"
)

// brTagRE matches <br> tags (case-insensitive, optional slash)
var brTagRE = regexp.MustCompile(`(?i)<br\s*/?>`)

// tableSepRowRE matches a markdown table separator row anchored to line
// boundaries: |---|---| or |:---|---:| with optional surrounding whitespace.
// Anchored so a data cell containing a literal "---" can't false-positive.
var tableSepRowRE = regexp.MustCompile(`^\s*\|?[\s\-:|]+\|[\s\-:|]+\s*$`)

// markdownTableBlockRE matches one or more contiguous lines starting with |, including separator rows
var markdownTableBlockRE = regexp.MustCompile(`(?m)(^\s*\|.*\|\s*$\n?)+`)

// blankCollapseRE collapses 3+ newlines into exactly 2
var blankCollapseRE = regexp.MustCompile(`\n{3,}`)

// NormalizeReplyForChatGateway cleans a model's reply for Discord/Telegram delivery.
// It preserves fenced code blocks verbatim (including the ``` fences so
// downstream code-fence-aware chunking still works), converts markdown
// tables to bullet lists, replaces <br> tags, collapses excessive newlines,
// and trims the final result. Inter-chunk whitespace between code blocks
// and surrounding prose is preserved so boundaries don't collapse.
func NormalizeReplyForChatGateway(s string) string {
	// Split the input into parts: code blocks and prose
	parts := splitOnFences(s)

	var result strings.Builder

	for _, part := range parts {
		if part.isCode {
			// Preserve code blocks verbatim, fences included
			result.WriteString(part.text)
		} else {
			// Process prose: convert tables, strip <br>, collapse newlines.
			// Do NOT TrimSpace per-chunk — that erases the space between
			// adjacent prose / code-block segments and collapses boundaries.
			processed := convertMarkdownTables(part.text)
			processed = brTagRE.ReplaceAllString(processed, " ")
			processed = blankCollapseRE.ReplaceAllString(processed, "\n\n")
			result.WriteString(processed)
		}
	}

	// Trim once at the end so leading/trailing whitespace on the whole
	// reply is normalized without smearing inter-part boundaries.
	return strings.TrimSpace(result.String())
}

// splitOnFences splits a string on triple-backtick fences. It returns a
// slice of fencePart, where isCode is true for code blocks. The fences
// themselves are included in the code-block text so downstream chunkers
// can detect code boundaries. If an opening fence has no closing one, the
// rest of the string (fence included) is treated as prose so no characters
// are silently dropped.
func splitOnFences(s string) []fencePart {
	var parts []fencePart
	start := 0

	for {
		// Find the next opening fence
		rel := strings.Index(s[start:], "```")
		if rel == -1 {
			// No more fences: add remaining text as prose
			if start < len(s) {
				parts = append(parts, fencePart{text: s[start:], isCode: false})
			}
			break
		}
		openIdx := start + rel

		// Emit prose before the fence
		if openIdx > start {
			parts = append(parts, fencePart{text: s[start:openIdx], isCode: false})
		}

		// Find the closing fence
		closeRel := strings.Index(s[openIdx+3:], "```")
		if closeRel == -1 {
			// No closing fence: keep the rest (fence included) as prose so
			// the raw text isn't silently mutated.
			parts = append(parts, fencePart{text: s[openIdx:], isCode: false})
			break
		}
		closeIdx := openIdx + 3 + closeRel

		// Include the fences in the captured code block so chunkers / round-
		// trip serializers see a syntactically intact fenced block.
		parts = append(parts, fencePart{text: s[openIdx : closeIdx+3], isCode: true})

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

			// Skip separator rows (line-anchored regex so a real data cell
			// containing "---" can't be misclassified).
			if tableSepRowRE.MatchString(line) {
				continue
			}

			// Split by | and remove empty first/last
			cells := strings.Split(line, "|")
			cells = cells[1 : len(cells)-1]

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
					parts = append(parts, "**"+headers[i]+"**: "+cell)
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
