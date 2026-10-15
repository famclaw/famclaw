package browser

import (
	"errors"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
)

// fakeResponse is a minimal playwright.Response stub for the wire-byte
// tests. It embeds the interface (left nil) and overrides only the two
// methods responseWireBytes touches — Headers and Body — so any other call
// panics loudly instead of silently succeeding. bodyCalls records whether
// the body was fetched (it must not be when Content-Length is present).
type fakeResponse struct {
	playwright.Response
	headers   map[string]string
	body      []byte
	bodyErr   error
	bodyCalls int
}

func (f *fakeResponse) Headers() map[string]string { return f.headers }
func (f *fakeResponse) Body() ([]byte, error) {
	f.bodyCalls++
	return f.body, f.bodyErr
}

// TestContentLength table-drives the Content-Length header parser: present
// and parseable values (including zero and surrounding whitespace) are
// accepted; absent, empty, non-numeric, negative, overflowing, or
// space-separated values all fall through to the received-body length.
func TestContentLength(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    int64
		wantOK  bool
	}{
		{"present", map[string]string{"content-length": "1234"}, 1234, true},
		{"present zero", map[string]string{"content-length": "0"}, 0, true},
		{"present padded", map[string]string{"content-length": "  42  "}, 42, true},
		{"absent", map[string]string{"transfer-encoding": "chunked"}, 0, false},
		{"empty value", map[string]string{"content-length": ""}, 0, false},
		{"whitespace only", map[string]string{"content-length": "   "}, 0, false},
		{"non-numeric", map[string]string{"content-length": "abc"}, 0, false},
		{"negative", map[string]string{"content-length": "-5"}, 0, false},
		{"overflow", map[string]string{"content-length": "99999999999999999999"}, 0, false},
		{"space separated", map[string]string{"content-length": "12 34"}, 0, false},
		{"nil headers", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := contentLength(tc.headers)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("contentLength(%v) = (%d, %v), want (%d, %v)",
					tc.headers, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestWireBodyBytes table-drives the Content-Length-vs-body-length rule:
// a parseable Content-Length wins (the received body length is ignored);
// otherwise the received body length is used — for a chunked
// transfer-encoding response that is the sum of the chunk payloads.
func TestWireBodyBytes(t *testing.T) {
	chunked := map[string]string{"transfer-encoding": "chunked"}
	cases := []struct {
		name      string
		headers   map[string]string
		bodyLen   int
		wantBytes int64
	}{
		{
			name:      "content-length wins over body length",
			headers:   map[string]string{"content-length": "777"},
			bodyLen:   100,
			wantBytes: 777,
		},
		{
			name:      "chunked no content-length uses body length",
			headers:   chunked,
			bodyLen:   1234,
			wantBytes: 1234,
		},
		{
			name:      "no headers empty body is zero",
			headers:   map[string]string{},
			bodyLen:   0,
			wantBytes: 0,
		},
		{
			name:      "unparseable content-length falls back to body length",
			headers:   map[string]string{"content-length": "n/a"},
			bodyLen:   42,
			wantBytes: 42,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wireBodyBytes(tc.headers, tc.bodyLen); got != tc.wantBytes {
				t.Errorf("wireBodyBytes(%v, %d) = %d, want %d",
					tc.headers, tc.bodyLen, got, tc.wantBytes)
			}
		})
	}
}

// TestResponseWireBytes exercises the full response -> wire-bytes path a
// NavigateAndExtract caller goes through. The chunked case is end to end at
// the unit level: a fake response with Transfer-Encoding: chunked, no
// Content-Length, and a body assembled from concatenated chunk payloads —
// the reported size must be the sum of those payloads.
func TestResponseWireBytes(t *testing.T) {
	chunk1 := []byte("hello ")
	chunk2 := []byte("world")
	chunkedBody := append(append([]byte{}, chunk1...), chunk2...) // 11 bytes

	cases := []struct {
		name       string
		resp       playwright.Response
		wantBytes  int64
		wantErr    bool
		wantNoBody bool // the body must not be fetched when Content-Length suffices
	}{
		{
			name: "nil response reports zero",
			resp: nil,
		},
		{
			name: "content-length present is reported without reading body",
			resp: &fakeResponse{
				headers: map[string]string{
					"content-length": "4242",
					"content-type":   "text/html",
				},
				// A large body: if it were read and used, the result
				// would be wrong, and bodyCalls would be non-zero.
				body:    make([]byte, 1000),
				bodyErr: errors.New("body must not be read when content-length is present"),
			},
			wantBytes:  4242,
			wantNoBody: true,
		},
		{
			name: "chunked transfer-encoding uses sum of chunk payloads",
			resp: &fakeResponse{
				headers: map[string]string{
					"transfer-encoding": "chunked",
					"content-type":      "text/html",
				},
				body: chunkedBody,
			},
			wantBytes: int64(len(chunk1) + len(chunk2)),
		},
		{
			name: "streamed no content-length empty body is zero",
			resp: &fakeResponse{
				headers: map[string]string{"transfer-encoding": "chunked"},
				body:    []byte{},
			},
		},
		{
			name: "body read failure is wrapped",
			resp: &fakeResponse{
				headers: map[string]string{"transfer-encoding": "chunked"},
				bodyErr: errors.New("connection closed"),
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := responseWireBytes(tc.resp)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "read response body") {
					t.Errorf("error %q is not the wrapped body-read error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantBytes {
				t.Errorf("responseWireBytes() = %d, want %d", got, tc.wantBytes)
			}
			if tc.wantNoBody {
				if fr, ok := tc.resp.(*fakeResponse); ok && fr.bodyCalls != 0 {
					t.Errorf("body was read %d time(s); want 0 when content-length is present", fr.bodyCalls)
				}
			}
		})
	}
}
