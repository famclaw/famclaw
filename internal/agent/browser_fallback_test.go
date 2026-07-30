package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/config"
)

// fakeBrowserPool is a BrowserFetcher for unit-testing the web_fetch browser
// fallback. It records every Exec call and returns whatever execFn produces.
type fakeBrowserPool struct {
	execFn func(ctx context.Context, in browser.ExecInput) (string, error)
	calls  []browser.ExecInput
}

func (f *fakeBrowserPool) Exec(ctx context.Context, in browser.ExecInput) (string, error) {
	f.calls = append(f.calls, in)
	if f.execFn != nil {
		return f.execFn(ctx, in)
	}
	return "", nil
}

// alwaysAllowHost accepts every host — used in fallback tests where the host
// gate is not the subject under test.
func alwaysAllowHost(host string) bool { return true }

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

// TestFetchWithBrowser_BrowserFetchFailure verifies the browser-fetch-failure
// path: when navigation errors, the fallback wraps and returns that error
// rather than swallowing it.
func TestFetchWithBrowser_BrowserFetchFailure(t *testing.T) {
	fake := &fakeBrowserPool{
		execFn: func(_ context.Context, in browser.ExecInput) (string, error) {
			if in.ToolName == "builtin__browser_navigate" {
				return "", fmt.Errorf("playwright: websocket closed")
			}
			return "irrelevant", nil
		},
	}
	a := newFallbackAgent()
	_, err := a.fetchWithBrowser(context.Background(), fake, "https://example.com/page", alwaysAllowHost)
	if err == nil {
		t.Fatal("expected error for browser fetch failure, got nil")
	}
	if !strings.Contains(err.Error(), "navigate") {
		t.Fatalf("error should mention navigate failure, got: %v", err)
	}
}

// TestFetchWithBrowser_ExtractFailure verifies that a failure during extraction
// (after a successful navigate) is surfaced as an honest error.
func TestFetchWithBrowser_ExtractFailure(t *testing.T) {
	fake := &fakeBrowserPool{
		execFn: func(_ context.Context, in browser.ExecInput) (string, error) {
			if in.ToolName == "builtin__browser_navigate" {
				return "", nil
			}
			return "", fmt.Errorf("page crashed during extract")
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
		execFn: func(_ context.Context, in browser.ExecInput) (string, error) {
			// Both navigate and extract succeed, but extract yields only
			// whitespace.
			return "   \n\t  ", nil
		},
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

// TestFetchWithBrowser_Success asserts the happy path: a non-empty rendered
// text is returned as a populated Result, not silently dropped or truncated.
func TestFetchWithBrowser_Success(t *testing.T) {
	const rendered = "JS-rendered content that the plain fetch could not see"
	fake := &fakeBrowserPool{
		execFn: func(_ context.Context, in browser.ExecInput) (string, error) {
			if in.ToolName == "builtin__browser_navigate" {
				return "", nil
			}
			return rendered, nil
		},
	}
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
	if res.Bytes != int64(len(rendered)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(rendered))
	}
	// Verify both navigate and extract were called.
	var navigateSeen, extractSeen bool
	for _, c := range fake.calls {
		switch c.ToolName {
		case "builtin__browser_navigate":
			navigateSeen = true
		case "builtin__browser_extract":
			extractSeen = true
		}
	}
	if !navigateSeen {
		t.Error("navigate was not called")
	}
	if !extractSeen {
		t.Error("extract was not called")
	}
}
