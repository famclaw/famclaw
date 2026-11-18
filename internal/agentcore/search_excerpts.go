package agentcore

import (
	"context"
	"html"
	"net/url"
	"strings"
	"unicode"
)

// SearchExcerpt carries provider text as data, separate from the formatted model result.
type SearchExcerpt struct{ Title, URL, Snippet string }
type searchExcerptKey struct{}
type searchExcerptSink struct{ values []SearchExcerpt }

// RecordSearchExcerpts records structured search output only when a caller supplied a sink.
func RecordSearchExcerpts(ctx context.Context, excerpts []SearchExcerpt) {
	sink, ok := ctx.Value(searchExcerptKey{}).(*searchExcerptSink)
	if !ok || sink == nil {
		return
	}
	for _, item := range excerpts {
		if len(sink.values) >= 3 {
			break
		}
		source := excerptURL(item.URL)
		if source == "" {
			continue
		}
		title, snippet := excerptText(item.Title, 120), excerptText(item.Snippet, 240)
		if title != "" || snippet != "" {
			sink.values = append(sink.values, SearchExcerpt{title, source, snippet})
		}
	}
}

// excerptText renders provider data as bounded inert text in Markdown and HTML.
func excerptText(s string, limit int) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	count := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if count >= limit {
			b.WriteRune('…')
			break
		}
		count++
		if strings.ContainsRune(`\\\`+"`"+`*_{}[]()#+-.!|>~`, r) {
			b.WriteByte('\\')
		}
		b.WriteString(html.EscapeString(string(r)))
	}
	return strings.TrimSpace(b.String())
}

// excerptURL only admits ordinary HTTP(S) links without Markdown delimiters or credentials.
func excerptURL(raw string) string {
	if len(raw) == 0 || len(raw) > 2048 {
		return ""
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>[]\\`\"'", r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	return u.String()
}

func renderSearchExcerpts(excerpts []SearchExcerpt) string {
	if len(excerpts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Search snippets (full pages not checked):")
	for _, item := range excerpts {
		title := item.Title
		if title == "" {
			title = "Source"
		}
		b.WriteString("\n[")
		b.WriteString(title)
		b.WriteString("](")
		b.WriteString(item.URL)
		b.WriteByte(')')
		if item.Snippet != "" {
			b.WriteString("\n\"")
			b.WriteString(item.Snippet)
			b.WriteByte('"')
		}
	}
	return b.String()
}
