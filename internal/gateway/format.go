package gateway

import (
	"regexp"
	"strings"
)

// brTagRE matches HTML <br> variants (case-insensitive). Discord and
// Telegram both render these as literal text, so we strip them.
var brTagRE = regexp.MustCompile(`(?i)<br\s*/?\s*>`)

// tableSepRowRE matches markdown table separator rows: |---|---| or |:---|---:|
// optionally with whitespace. These appear as literal text in Discord/Telegram
// even when the surrounding header/data rows render via pipes.
var tableSepRowRE = regexp.MustCompile(`(?m)^\s*\|?[\s\-:|]+\|[\s\-:|]+$`)

// markdownTableBlockRE matches a contiguous run of markdown table rows
// (lines starting with `|`). Captures one block at a time so we can convert
// each table independently.
var markdownTableBlockRE = regexp.MustCompile(`(?m)(^\s*\|.*\|\s*$\n?)+`)

// NormalizeReplyForChatGateway cleans a model reply for delivery to a
// chat gateway (Discord / Telegram). It:
//   - strips HTML <br> tags (they render as literal text)
//   - converts markdown tables to bullet lists (they render with literal
//     pipes and broken separators on Discord; render as plain text with
//     pipes on Telegram)
//   - normalizes excess blank lines
//
// The function is conservative: it does not touch code blocks (text inside
// triple-backtick fences is preserved verbatim) and does not strip headers
// or bold/italic markers — Discord renders those natively, and the per-
// gateway adapter handles Telegram conversion.
func NormalizeReplyForChatGateway(s string) string {
	if s == "" {
		return s
	}
	// Split out fenced code blocks so we don't transform their contents.
	parts := splitOnFences(s)
	for i, p := range parts {
		if p.isCode {
			continue
		}
		// Order matters: convert tables FIRST so the table converter can
		// transform <br> inside cells into " / ". Then strip any remaining
		// <br> in prose.
		p.text = convertMarkdownTables(p.text)
		p.text = brTagRE.ReplaceAllString(p.text, "")
		parts[i] = p
	}
	out := joinFences(parts)
	// Collapse 3+ blank lines down to 2.
	out = regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// fencePart represents a slice of the message — either prose or a fenced
// code block. We preserve code-block content verbatim because the model
// uses fences intentionally (e.g. wrapping a real table for Discord) and
// we don't want to fight that choice.
type fencePart struct {
	text   string
	isCode bool
}

// splitOnFences walks the string and emits a sequence of (prose, code,
// prose, code, ...) segments based on triple-backtick fences. Unbalanced
// fences are treated as prose (we never let an unterminated fence consume
// the rest of the message).
func splitOnFences(s string) []fencePart {
	var out []fencePart
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			out = append(out, fencePart{text: s})
			return out
		}
		// Emit the prose before the fence.
		if start > 0 {
			out = append(out, fencePart{text: s[:start]})
		}
		// Find the closing fence on a later line.
		rest := s[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			// No closing fence — treat the rest as prose.
			out = append(out, fencePart{text: s[start:]})
			return out
		}
		block := s[start : start+3+end+3]
		out = append(out, fencePart{text: block, isCode: true})
		s = rest[end+3:]
	}
}

// joinFences reassembles the parts produced by splitOnFences.
func joinFences(parts []fencePart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.text)
	}
	return b.String()
}

// convertMarkdownTables finds contiguous runs of `|...|` lines and rewrites
// them as bullet lists. The first row is treated as the header; subsequent
// rows produce one bullet per row with `header: value` pairs separated by
// "  •  " for readability. Separator rows (`|---|`) are dropped.
func convertMarkdownTables(s string) string {
	return markdownTableBlockRE.ReplaceAllStringFunc(s, func(block string) string {
		rows := strings.Split(strings.TrimSpace(block), "\n")
		// Parse each row into cells (trim leading/trailing pipes + whitespace).
		var parsed [][]string
		for _, row := range rows {
			if tableSepRowRE.MatchString(row) {
				continue
			}
			trimmed := strings.TrimSpace(row)
			trimmed = strings.TrimPrefix(trimmed, "|")
			trimmed = strings.TrimSuffix(trimmed, "|")
			cells := strings.Split(trimmed, "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
				// Inside a cell, the model sometimes uses <br> for line
				// breaks — convert to literal " / " separator.
				cells[i] = brTagRE.ReplaceAllString(cells[i], " / ")
			}
			parsed = append(parsed, cells)
		}
		if len(parsed) == 0 {
			return ""
		}
		// First row = headers. If only one row exists, emit it as a bullet
		// line with no header pairing.
		if len(parsed) == 1 {
			return "- " + strings.Join(parsed[0], " — ") + "\n"
		}
		headers := parsed[0]
		var b strings.Builder
		b.WriteByte('\n')
		for _, row := range parsed[1:] {
			b.WriteString("- ")
			for i, cell := range row {
				if i >= len(headers) {
					break
				}
				if i > 0 {
					b.WriteString("  •  ")
				}
				if headers[i] != "" {
					b.WriteString("**")
					b.WriteString(stripMarkers(headers[i]))
					b.WriteString("**: ")
				}
				b.WriteString(cell)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
		return b.String()
	})
}

// stripMarkers removes outer `**` / `__` / `*` / `_` from a header cell so
// the converted bullet doesn't double up on bold markers.
func stripMarkers(s string) string {
	for _, pair := range []string{"**", "__", "*", "_"} {
		if strings.HasPrefix(s, pair) && strings.HasSuffix(s, pair) && len(s) > 2*len(pair) {
			s = strings.TrimPrefix(s, pair)
			s = strings.TrimSuffix(s, pair)
		}
	}
	return s
}
