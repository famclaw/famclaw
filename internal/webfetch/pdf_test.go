package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture loads a checked-in testdata file.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestExtractPDFText is table-driven over the real fixtures checked into
// testdata: a two-page text-layered PDF must round-trip its text layer, a
// no-text-layer PDF must yield the distinguishable note (not an error), and
// garbage input must yield an honest wrapped error.
func TestExtractPDFText(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		wantSub    string
		wantErrSub string
	}{
		{
			name:    "round-trips text-layered fixture (page one)",
			data:    readFixture(t, "sample.pdf"),
			wantSub: "the quick brown fox jumps over the lazy dog",
		},
		{
			name:    "round-trips second page too",
			data:    readFixture(t, "sample.pdf"),
			wantSub: "Page two text",
		},
		{
			name:    "no text layer yields distinguishable note, not an error",
			data:    readFixture(t, "notextlayer.pdf"),
			wantSub: "no text layer",
		},
		{
			name:       "garbage bytes yield honest error",
			data:       []byte("%PDF-1.4\nthis is not really a pdf file"),
			wantErrSub: "open pdf",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractPDFText(tt.data)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (text=%q)", tt.wantErrSub, got)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Fatalf("text missing %q: %q", tt.wantSub, got)
			}
		})
	}
}

// TestExtractPDFText_NoTextLayerIsExactNote pins the no-text-layer result to
// the exact note so downstream code (and tests) can match on it: a scanned
// or image-only PDF is a coverage limit, and it must never be masked as a
// fetch failure or as text that is really absent.
func TestExtractPDFText_NoTextLayerIsExactNote(t *testing.T) {
	got, err := ExtractPDFText(readFixture(t, "notextlayer.pdf"))
	if err != nil {
		t.Fatalf("no-text-layer PDF must not be an error: %v", err)
	}
	if got != pdfNoTextLayerNote {
		t.Fatalf("text = %q, want exact note %q", got, pdfNoTextLayerNote)
	}
}

// TestFetch_PDF serves the real text-layered fixture over httptest (no
// network, no live keys) and verifies the end-to-end path: application/pdf
// is now on the default allowlist and the body is extracted to readable
// text.
func TestFetch_PDF(t *testing.T) {
	data := readFixture(t, "sample.pdf")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", res.StatusCode)
	}
	if res.ContentType != "application/pdf" {
		t.Errorf("content type: got %q", res.ContentType)
	}
	if res.Truncated {
		t.Error("small fixture should not be truncated")
	}
	if !strings.Contains(res.Text, "the quick brown fox jumps over the lazy dog") {
		t.Errorf("missing expected text: %q", res.Text)
	}
}

// TestFetch_PDF_NoTextLayer verifies the no-text-layer fixture fetches
// without error and surfaces the distinguishable "no text layer" note as the
// text — a coverage limit, not a failure and not a panic.
func TestFetch_PDF_NoTextLayer(t *testing.T) {
	data := readFixture(t, "notextlayer.pdf")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
	if err != nil {
		t.Fatalf("no-text-layer PDF must not be a fetch error: %v", err)
	}
	if res.Text != pdfNoTextLayerNote {
		t.Fatalf("text = %q, want the no-text-layer note", res.Text)
	}
}

// TestFetch_PDF_ByteCap verifies the MaxBytes cap applies to PDF bodies: a
// PDF larger than MaxBytes is capped and flagged Truncated, and the partial
// body — which cannot be parsed (the xref table sits at the end of the
// file) — yields an honest, actionable size error rather than a panic or a
// fake "empty success".
func TestFetch_PDF_ByteCap(t *testing.T) {
	data := readFixture(t, "sample.pdf")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data)
	}))
	defer srv.Close()

	half := int64(len(data)/2) + 1
	res, err := Fetch(context.Background(), srv.URL, localOpts(Options{MaxBytes: half}))
	if res == nil {
		t.Fatalf("expected a (truncated) result, got nil (err=%v)", err)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true for a PDF over the cap")
	}
	if err == nil {
		t.Fatalf("a truncated PDF cannot be parsed; expected an honest size error, got nil (text=%q)", res.Text)
	}
	if !strings.Contains(err.Error(), "truncated PDF cannot be parsed") {
		t.Errorf("error should name the truncation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("error should point at the max_bytes cap, got: %v", err)
	}
}
