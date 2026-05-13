package gateway

import (
	"strings"
	"testing"
)

func TestNormalizeStripsBRTags(t *testing.T) {
	in := "first line<br>second<br/>third<BR />fourth"
	got := NormalizeReplyForChatGateway(in)
	if strings.Contains(strings.ToLower(got), "<br") {
		t.Errorf("expected <br> stripped, got %q", got)
	}
}

func TestNormalizeConvertsMarkdownTable(t *testing.T) {
	in := "| Name | Age |\n| --- | --- |\n| Alice | 30 |\n| Bob | 25 |\n"
	got := NormalizeReplyForChatGateway(in)
	if strings.Contains(got, "| Alice |") {
		t.Errorf("table row pipe survived: %q", got)
	}
	if !strings.Contains(got, "Alice") || !strings.Contains(got, "30") {
		t.Errorf("table content lost: %q", got)
	}
	if !strings.Contains(got, "- ") {
		t.Errorf("expected bullet output, got %q", got)
	}
}

func TestNormalizeDropsTableSeparatorRow(t *testing.T) {
	in := "| H1 | H2 |\n|:---|---:|\n| a | b |"
	got := NormalizeReplyForChatGateway(in)
	if strings.Contains(got, ":---") || strings.Contains(got, "---:") {
		t.Errorf("separator row leaked: %q", got)
	}
}

func TestNormalizeBRInTableCellBecomesSlash(t *testing.T) {
	in := "| Col1 | Col2 |\n| --- | --- |\n| one<br>two | three<br/>four |"
	got := NormalizeReplyForChatGateway(in)
	if !strings.Contains(got, "one / two") {
		t.Errorf("<br> in cell should become ' / ', got %q", got)
	}
}

func TestNormalizePreservesCodeBlockContents(t *testing.T) {
	in := "before\n```\n| this | should | survive |\n| --- | --- | --- |\n| a | b | c |\n```\nafter"
	got := NormalizeReplyForChatGateway(in)
	if !strings.Contains(got, "| this | should | survive |") {
		t.Errorf("table inside code block should be preserved verbatim, got %q", got)
	}
	if !strings.Contains(got, "| --- | --- | --- |") {
		t.Errorf("separator inside code block should be preserved, got %q", got)
	}
}

func TestNormalizePreservesCodeBlockBR(t *testing.T) {
	in := "look at this code: ```html\nfoo<br>bar\n```"
	got := NormalizeReplyForChatGateway(in)
	if !strings.Contains(got, "foo<br>bar") {
		t.Errorf("<br> inside code block should be preserved (it's literal code), got %q", got)
	}
}

func TestNormalizeCollapsesExcessBlankLines(t *testing.T) {
	in := "para 1\n\n\n\n\npara 2"
	got := NormalizeReplyForChatGateway(in)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("expected blank lines collapsed, got %q", got)
	}
}

func TestNormalizeEmptyInput(t *testing.T) {
	if got := NormalizeReplyForChatGateway(""); got != "" {
		t.Errorf("empty input should pass through, got %q", got)
	}
}

func TestNormalizeUnbalancedFenceFallsBackToProse(t *testing.T) {
	// Unterminated fence: everything after ``` is treated as prose. We
	// don't try to "fix" the message; we just don't let it crash.
	in := "before\n```\n| a | b |\n| - | - |\n| 1 | 2 |"
	got := NormalizeReplyForChatGateway(in)
	// As prose-after-fence: the unterminated fence content is preserved.
	// We just verify the function returned without panic and didn't drop everything.
	if got == "" {
		t.Errorf("expected non-empty output, got %q", got)
	}
}

func TestNormalizeRealisticDiscordResponse(t *testing.T) {
	// The kind of thing Butler emits today.
	in := `## Comparison

| **Feature** | **Austin, TX** | **New Orleans, LA** |
|-------------|----------------|---------------------|
| **Sweets**  | Ice cream<br>Churros | Beignets<br>Pralines |
| **Music**   | Live country | Jazz                 |

Both have great vibes.`
	got := NormalizeReplyForChatGateway(in)
	// Table should be gone
	if strings.Contains(got, "|---") || strings.Contains(got, "| **Feature**") {
		t.Errorf("table not converted: %q", got)
	}
	// Content should remain
	for _, want := range []string{"Austin", "New Orleans", "Beignets", "Pralines"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost content %q in: %q", want, got)
		}
	}
	// <br> should be replaced
	if strings.Contains(strings.ToLower(got), "<br") {
		t.Errorf("<br> survived: %q", got)
	}
	// Trailing paragraph should still be there
	if !strings.Contains(got, "Both have great vibes") {
		t.Errorf("trailing prose lost: %q", got)
	}
}
