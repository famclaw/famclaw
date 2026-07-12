package telegram

import (
	"regexp"
	"strings"
)

// markdownToTelegramHTML converts a subset of markdown to the HTML tags
// Telegram supports under parse_mode="HTML": <b>, <i>, <code>, <pre>.
// Other characters are HTML-escaped so literal <, >, & don't break the
// parser.
//
// Conversion strategy:
//  1. Pull out triple-backtick fences and single-backtick spans into
//     placeholders so we can HTML-escape their contents separately.
//  2. HTML-escape the remaining prose.
//  3. Convert markdown emphasis markers (** __ * _) to <b> / <i>.
//  4. Restore the code placeholders with their escaped contents wrapped
//     in <pre> / <code>.
//
// Why pulled placeholders: if we escaped first then converted, the model's
// **bold** markers would still work — but a markdown `*important*` span
// surrounding a literal `<` would have its escaped `&lt;` between the
// markers. That's fine. The problem is code content: “ `<x>` “ must
// render the literal `<x>` inside a <code> tag, not be doubly-escaped or
// mistaken for an HTML tag. Pulling code spans out first preserves them.
var (
	tripleFenceRE = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n?(.*?)```")
	singleCodeRE  = regexp.MustCompile("`([^`\n]+)`")
	boldStarRE    = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	italicStarRE  = regexp.MustCompile(`(^|[^*])\*([^*\n]+?)\*([^*]|$)`)
	boldUnderRE   = regexp.MustCompile(`__([^_\n]+?)__`)
	italicUnderRE = regexp.MustCompile(`(^|[^_])_([^_\n]+?)_([^_]|$)`)
	// Strip leading "# ", "## ", "### " markers — Telegram has no header
	// concept, but bolding the line approximates the visual weight.
	headerRE = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
)

func markdownToTelegramHTML(s string) string {
	if s == "" {
		return s
	}

	// 1. Pull out triple-fence blocks.
	type capture struct {
		content string
		pre     bool
	}
	var caps []capture
	s = tripleFenceRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := tripleFenceRE.FindStringSubmatch(m)
		caps = append(caps, capture{content: sub[1], pre: true})
		return placeholder(len(caps) - 1)
	})
	// 2. Pull out single-backtick spans.
	s = singleCodeRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := singleCodeRE.FindStringSubmatch(m)
		caps = append(caps, capture{content: sub[1], pre: false})
		return placeholder(len(caps) - 1)
	})

	// 3. HTML-escape the rest. We do this BEFORE converting emphasis so
	//    literal < or & inside prose becomes safe entities.
	s = htmlEscape(s)

	// 4. Markdown emphasis → HTML. Order matters: bold (** / __) before
	//    italic (* / _) so `**foo**` doesn't get partially consumed.
	s = boldStarRE.ReplaceAllString(s, "<b>$1</b>")
	s = boldUnderRE.ReplaceAllString(s, "<b>$1</b>")
	s = italicStarRE.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = italicUnderRE.ReplaceAllString(s, "$1<i>$2</i>$3")

	// 5. Headers → bold. Telegram has no header tag.
	s = headerRE.ReplaceAllString(s, "<b>$2</b>")

	// 6. Restore code placeholders. Code content is HTML-escaped here
	//    (not before, so triple-fence content with `<` survives as the
	//    literal user expects).
	for i, c := range caps {
		ph := placeholder(i)
		escaped := htmlEscape(c.content)
		if c.pre {
			s = strings.ReplaceAll(s, ph, "<pre>"+escaped+"</pre>")
		} else {
			s = strings.ReplaceAll(s, ph, "<code>"+escaped+"</code>")
		}
	}

	return s
}

// placeholder returns a string unlikely to appear in real model output.
// Using a Unicode private-use area sentinel keeps it from colliding with
// anything the model emits.
func placeholder(i int) string {
	return "FCPH" + indexStr(i) + ""
}

func indexStr(i int) string {
	if i == 0 {
		return "0"
	}
	var b strings.Builder
	for i > 0 {
		b.WriteByte(byte('0' + i%10))
		i /= 10
	}
	// reverse
	bytes := []byte(b.String())
	for l, r := 0, len(bytes)-1; l < r; l, r = l+1, r-1 {
		bytes[l], bytes[r] = bytes[r], bytes[l]
	}
	return string(bytes)
}

// htmlEscape replaces the three characters Telegram HTML mode requires
// escaped: &, <, >. We don't escape quotes — Telegram doesn't need it
// in body text (only in attribute values, and we don't emit raw <a>
// tags here).
func htmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
