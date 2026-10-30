package agent

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/webfetch"
)

// fakeNavCall records one NavigateAndExtract invocation for assertion.
type fakeNavCall struct {
	user      string
	rawURL    string
	hostCheck browser.HostValidator
}

// fakeBrowserPool is a BrowserFetcher for unit-testing the web_fetch browser
// fallback. It records every NavigateAndExtract call and returns whatever
// navFn produces (or the fixed text/bytes when navFn is nil). Exec exists to
// satisfy the interface (the builtin__browser_* dispatch) but the fallback
// path never calls it.
type fakeBrowserPool struct {
	navFn func(ctx context.Context, user, rawURL string, hostCheck browser.HostValidator) (string, int64, error)
	calls []fakeNavCall
	text  string
	bytes int64
}

func (f *fakeBrowserPool) NavigateAndExtract(ctx context.Context, user, rawURL string, hostCheck browser.HostValidator) (string, int64, error) {
	f.calls = append(f.calls, fakeNavCall{user: user, rawURL: rawURL, hostCheck: hostCheck})
	if f.navFn != nil {
		return f.navFn(ctx, user, rawURL, hostCheck)
	}
	return f.text, f.bytes, nil
}

// Exec satisfies the BrowserFetcher interface. The web_fetch fallback path
// never dispatches builtin__browser_* through it.
func (f *fakeBrowserPool) Exec(_ context.Context, _ browser.ExecInput) (string, error) {
	return "", fmt.Errorf("fakeBrowserPool: Exec is not used by the web_fetch fallback path")
}

// alwaysAllowHost accepts every host — used in fallback tests where the host
// gate is not the subject under test.
func alwaysAllowHost(_ context.Context, host string) error { return nil }

func newFallbackAgent() *Agent {
	return &Agent{
		user: &config.UserConfig{Name: "testuser", Role: "parent"},
	}
}

// TestFetchWithBrowser_NilPool verifies the nil/unavailable-pool failure path:
// when no browser pool is wired, the fallback returns a clear error rather
// than a nil result that the caller might mistake for an empty success.
func TestFetchWithBrowser_NilPool(t *testing.T) {
	a := newFallbackAgent()
	_, err := a.fetchWithBrowser(context.Background(), nil, "https://example.com/page", alwaysAllowHost)
	if err == nil {
		t.Fatal("expected error for nil pool, got nil")
	}
	if !strings.Contains(err.Error(), "browser pool is not configured") {
		t.Fatalf("error should mention browser pool not configured, got: %v", err)
	}
}

// TestFetchWithBrowser_NavigateFailure verifies the browser-fetch-failure
// path: when the pool's navigate/extract call errors, the fallback wraps and
// returns that error rather than swallowing it.
func TestFetchWithBrowser_NavigateFailure(t *testing.T) {
	fake := &fakeBrowserPool{
		navFn: func(_ context.Context, _, _ string, _ browser.HostValidator) (string, int64, error) {
			return "", 0, fmt.Errorf("playwright: websocket closed")
		},
	}
	a := newFallbackAgent()
	_, err := a.fetchWithBrowser(context.Background(), fake, "https://example.com/page", alwaysAllowHost)
	if err == nil {
		t.Fatal("expected error for browser fetch failure, got nil")
	}
	if !strings.Contains(err.Error(), "browser fetch") || !strings.Contains(err.Error(), "websocket closed") {
		t.Fatalf("error should carry the underlying pool error, got: %v", err)
	}
}

// TestFetchWithBrowser_ExtractFailure verifies that a failure during
// extraction (mid-call) is surfaced as an honest error, not swallowed.
func TestFetchWithBrowser_ExtractFailure(t *testing.T) {
	fake := &fakeBrowserPool{
		navFn: func(_ context.Context, _, _ string, _ browser.HostValidator) (string, int64, error) {
			return "", 0, fmt.Errorf("page crashed during extract")
		},
	}
	a := newFallbackAgent()
	_, err := a.fetchWithBrowser(context.Background(), fake, "https://example.com/page", alwaysAllowHost)
	if err == nil {
		t.Fatal("expected error for extract failure, got nil")
	}
	if !strings.Contains(err.Error(), "extract") {
		t.Fatalf("error should mention extract failure, got: %v", err)
	}
}

// TestFetchWithBrowser_EmptyRender verifies the empty-render failure path:
// when the browser navigates but renders no text, the fallback returns an
// error — never an empty "success" that looks like an empty page was fetched.
func TestFetchWithBrowser_EmptyRender(t *testing.T) {
	fake := &fakeBrowserPool{
		text: "   \n\t  ", // whitespace only
	}
	a := newFallbackAgent()
	_, err := a.fetchWithBrowser(context.Background(), fake, "https://example.com/page", alwaysAllowHost)
	if err == nil {
		t.Fatal("expected error for empty render, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should mention empty render, got: %v", err)
	}
}

// TestFetchWithBrowser_Success asserts the happy path and the wire-bytes
// semantics: a non-empty rendered text is returned as a populated Result, and
// Bytes is the pool-reported size of the navigated page's HTTP response body
// in transferred (wire) bytes — deliberately NOT the extracted-text length
// (issue #246 review).
func TestFetchWithBrowser_Success(t *testing.T) {
	const rendered = "JS-rendered content that the plain fetch could not see"
	const wireBytes = int64(4821) // the page's HTML, larger than the text
	fake := &fakeBrowserPool{text: rendered, bytes: wireBytes}
	a := newFallbackAgent()
	res, err := a.fetchWithBrowser(context.Background(), fake, "https://example.com/page", alwaysAllowHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if res.Text != rendered {
		t.Errorf("Text = %q, want %q", res.Text, rendered)
	}
	if res.URL != "https://example.com/page" {
		t.Errorf("URL = %q, want %q", res.URL, "https://example.com/page")
	}
	if res.Bytes != wireBytes {
		t.Errorf("Bytes = %d, want %d (wire bytes from the pool, not the text length %d)", res.Bytes, wireBytes, len(rendered))
	}
	// One pool call does navigate and extract.
	if len(fake.calls) != 1 {
		t.Fatalf("want exactly 1 NavigateAndExtract call, got %d", len(fake.calls))
	}
	if got := fake.calls[0].user; got != "testuser" {
		t.Errorf("user = %q, want %q", got, "testuser")
	}
	if got := fake.calls[0].rawURL; got != "https://example.com/page" {
		t.Errorf("rawURL = %q, want %q", got, "https://example.com/page")
	}
}

// newBotBlockAgent builds an agent for the 403/429 browser-fallback dispatch
// tests: web_fetch enabled with a non-empty allowlist (example.com), a stub
// webFetcher that returns the given HTTP status (plus a webfetch-style
// non-2xx error when status != 0), and an optional fake browser pool. A nil
// fake leaves browserPool a true nil (the "no pool" paths).
func newBotBlockAgent(status int, fallback bool, fake *fakeBrowserPool) *Agent {
	a := &Agent{
		user: &config.UserConfig{Name: "testuser", Role: "parent"},
		cfg: &config.Config{
			Tools: config.ToolsConfig{
				WebFetch: config.WebFetchConfig{
					Enabled:               true,
					URLAllowlist:          []string{"example.com"},
					MaxBytes:              256 * 1024,
					TimeoutSec:            5,
					FallbackToBrowser:     fallback,
					FallbackMinTextLength: 10,
				},
			},
		},
		webFetcher: func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
			if status != 0 {
				// Mirrors webfetch.Fetch: non-2xx returns the result (with
				// the status) plus an error.
				return &webfetch.Result{URL: "https://example.com/x", StatusCode: status, ContentType: "text/html"},
					fmt.Errorf("web fetch: non-2xx status %d", status)
			}
			return &webfetch.Result{URL: "https://example.com/x", StatusCode: status, ContentType: "text/plain", Text: "ok"}, nil
		},
	}
	if fake != nil {
		a.browserPool = fake
	}
	return a
}

// TestHandleWebFetch_BotBlockedBrowserFallback covers the F7 fix: a 403 or
// 429 bot-blocked page must reach the EXISTING browser fallback when
// fallback_to_browser is on and a pool exists, and the rendered text must be
// returned. It also asserts the fallback URL was gated by the same host
// allowlist (NavigateAndExtract carries a HostCheck that accepts an
// allowlisted host and rejects a non-allowlisted one).
func TestHandleWebFetch_BotBlockedBrowserFallback(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"403 bot-blocked page", 403},
		{"429 rate-limited page", 429},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const rendered = "Browser-rendered text of the bot-blocked page"
			var gateChecked bool
			fake := &fakeBrowserPool{
				bytes: 512,
				navFn: func(ctx context.Context, _, _ string, hostCheck browser.HostValidator) (string, int64, error) {
					if hostCheck == nil {
						t.Error("NavigateAndExtract must carry a HostCheck (the existing host gate)")
						return "", 0, fmt.Errorf("no host gate")
					}
					if err := hostCheck(ctx, "example.com"); err != nil {
						t.Errorf("allowlisted host rejected by fallback gate: %v", err)
					}
					if err := hostCheck(ctx, "evil.example"); err == nil {
						t.Error("non-allowlisted host accepted by fallback gate")
					}
					gateChecked = true
					return rendered, 512, nil
				},
			}
			a := newBotBlockAgent(tt.status, true, fake)
			out, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
			if err != nil {
				t.Fatalf("want browser text, got error: %v", err)
			}
			if !strings.Contains(out, rendered) {
				t.Errorf("output missing browser-rendered text: %q", out)
			}
			if len(fake.calls) != 1 {
				t.Errorf("want exactly 1 NavigateAndExtract call, got %d", len(fake.calls))
			}
			if !gateChecked {
				t.Error("host gate was never checked during the fallback")
			}
		})
	}
}

// TestHandleWebFetch_BotBlockedNoFallbackForOtherStatuses verifies the
// fallback fires ONLY for 403/429: a 404 or 5xx returns the original error
// and never calls the browser pool (0 calls observed).
func TestHandleWebFetch_BotBlockedNoFallbackForOtherStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"404 not found", 404},
		{"503 server error", 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeBrowserPool{}
			a := newBotBlockAgent(tt.status, true, fake)
			_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
			if err == nil {
				t.Fatalf("expected the original HTTP %d error, got nil", tt.status)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", tt.status)) {
				t.Errorf("error should carry the original status %d, got: %v", tt.status, err)
			}
			if len(fake.calls) != 0 {
				t.Errorf("browser fallback must NOT fire for HTTP %d; got %d pool calls", tt.status, len(fake.calls))
			}
		})
	}
}

// TestHandleWebFetch_BotBlocked403NoPool verifies the no-pool degradation: a
// 403 with fallback enabled but no browser pool returns the 403 error
// honestly (and logs it) — no panic, no empty success.
func TestHandleWebFetch_BotBlocked403NoPool(t *testing.T) {
	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOut)

	a := newBotBlockAgent(403, true, nil)
	_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want the original 403 error, got: %v", err)
	}
	if !strings.Contains(logBuf.String(), "web_fetch") {
		t.Errorf("expected the fetch failure to be logged, got: %q", logBuf.String())
	}
}

// TestHandleWebFetch_BotBlocked403FallbackDisabled verifies that with
// fallback_to_browser=false a 403 never touches the browser pool — the
// original error is returned.
func TestHandleWebFetch_BotBlocked403FallbackDisabled(t *testing.T) {
	fake := &fakeBrowserPool{}
	a := newBotBlockAgent(403, false, fake)
	_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want the original 403 error, got: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("fallback disabled; got %d pool calls, want 0", len(fake.calls))
	}
}

// TestHandleWebFetch_BotBlocked403FallbackFailsOriginalError verifies that
// when the browser fallback itself fails (pool error), the ORIGINAL 403
// error is returned — no empty masking, no fallback error substitution.
func TestHandleWebFetch_BotBlocked403FallbackFailsOriginalError(t *testing.T) {
	fake := &fakeBrowserPool{
		navFn: func(_ context.Context, _, _ string, _ browser.HostValidator) (string, int64, error) {
			return "", 0, fmt.Errorf("playwright: browser unavailable")
		},
	}
	a := newBotBlockAgent(403, true, fake)
	_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want the original 403 error, got: %v", err)
	}
	if len(fake.calls) == 0 {
		t.Error("the browser pool should have been attempted before giving up")
	}
}

// TestHandleWebFetch_BotBlockedShortRenderNotRetried verifies the
// no-double-render guard: after a 403 fallback produced a (short) non-empty
// browser render, the thin-fetch fallback must NOT navigate a second time —
// the browser render is authoritative for this call.
func TestHandleWebFetch_BotBlockedShortRenderNotRetried(t *testing.T) {
	fake := &fakeBrowserPool{
		navFn: func(_ context.Context, _, _ string, _ browser.HostValidator) (string, int64, error) {
			return "ok", 2, nil // non-empty but below the 10-char thin threshold
		},
	}
	a := newBotBlockAgent(403, true, fake)
	out, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.com/x"})
	if err != nil {
		t.Fatalf("short non-empty browser render is a success, got: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("output missing the browser text: %q", out)
	}
	if len(fake.calls) != 1 {
		t.Errorf("a short browser render must not re-trigger the fallback; got %d pool calls, want exactly 1", len(fake.calls))
	}
}
