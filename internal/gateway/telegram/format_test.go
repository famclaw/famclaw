package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownToHTMLBold(t *testing.T) {
	got := markdownToTelegramHTML("hello **world**")
	if got != "hello <b>world</b>" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLItalic(t *testing.T) {
	got := markdownToTelegramHTML("hello *world* end")
	if got != "hello <i>world</i> end" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLBoldUnderscore(t *testing.T) {
	got := markdownToTelegramHTML("hello __world__")
	if got != "hello <b>world</b>" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLInlineCode(t *testing.T) {
	got := markdownToTelegramHTML("call `foo()` now")
	if got != "call <code>foo()</code> now" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLFencedCode(t *testing.T) {
	got := markdownToTelegramHTML("see:\n```\nx := 1\n```")
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "x := 1") || !strings.Contains(got, "</pre>") {
		t.Errorf("expected <pre>x := 1</pre>, got %q", got)
	}
}

func TestMarkdownToHTMLEscapesAngleBrackets(t *testing.T) {
	got := markdownToTelegramHTML("use a < b and a > c")
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&gt;") {
		t.Errorf("expected escaped < and >, got %q", got)
	}
}

func TestMarkdownToHTMLEscapesAmpersand(t *testing.T) {
	got := markdownToTelegramHTML("Tom & Jerry")
	if got != "Tom &amp; Jerry" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLPreservesAngleBracketsInsideCode(t *testing.T) {
	// Inside `code` we want the literal <x> to render as <x> in the code
	// box. That means it must be escaped (so Telegram's HTML parser doesn't
	// try to parse it) but appear as < and > in the rendered output.
	got := markdownToTelegramHTML("type `<x>` is")
	if !strings.Contains(got, "<code>&lt;x&gt;</code>") {
		t.Errorf("expected <code>&lt;x&gt;</code>, got %q", got)
	}
}

func TestMarkdownToHTMLHeaderBecomesBold(t *testing.T) {
	got := markdownToTelegramHTML("# Hello World\nsome text")
	if !strings.Contains(got, "<b>Hello World</b>") {
		t.Errorf("expected header → bold, got %q", got)
	}
}

func TestMarkdownToHTMLBoldNotConsumedByItalic(t *testing.T) {
	// **foo** should NOT be partially matched as *foo* italic — order
	// of regex application matters.
	got := markdownToTelegramHTML("**important**")
	if got != "<b>important</b>" {
		t.Errorf("got %q", got)
	}
}

func TestMarkdownToHTMLEmpty(t *testing.T) {
	if got := markdownToTelegramHTML(""); got != "" {
		t.Errorf("empty input should pass through, got %q", got)
	}
}

func TestMarkdownToHTMLPreservesPlainText(t *testing.T) {
	got := markdownToTelegramHTML("just plain text, nothing special")
	if got != "just plain text, nothing special" {
		t.Errorf("plain text mangled: %q", got)
	}
}
