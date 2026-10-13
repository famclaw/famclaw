package webfetch

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pdfNoTextLayerNote is the placeholder text returned by ExtractPDFText when
// the document parses but carries no extractable text layer (scanned or
// image-only PDFs). This is NOT an error: the fetch succeeded and the
// document is intact — it simply has no text to read. The note is returned
// as the response text so the LLM can tell "document has no text layer"
// apart from a genuine fetch failure.
const pdfNoTextLayerNote = "[pdf: no text layer found — this document appears to be scanned or image-only; no readable text could be extracted]"

// ExtractPDFText extracts the text layer from a PDF body.
//
// It uses the pure-Go parser github.com/ledongthuc/pdf (no CGO) so the
// single binary keeps cross-compiling with CGO_ENABLED=0.
//
// Coverage limit: scanned/image-only PDFs have no text layer, so the
// returned text is the no-text-layer note. That is a coverage limit, not an
// error — the caller must not mask it as a fetch failure.
func ExtractPDFText(data []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("web fetch: open pdf: %w", err)
	}
	var b strings.Builder
	for i := 1; i <= reader.NumPage(); i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("web fetch: extract pdf page %d: %w", i, err)
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return pdfNoTextLayerNote, nil
	}
	return text, nil
}
