//go:build integration

package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/config"
)

// TestFetchWithBrowser_Integration verifies the browser fallback against a real
// Playwright server. It serves a JS-heavy page (pre-render text differs from
// post-render text) and asserts the fallback returns the JS-rendered content.
//
// Skipped when FAMCLAW_PLAYWRIGHT_WS is unset or the browser pool cannot be
// created, so the unit suite (go test ./...) is green without browser
// infrastructure. Run with:
//
//	go test -tags integration ./internal/agent/ -run TestFetchWithBrowser_Integration -v
func TestFetchWithBrowser_Integration(t *testing.T) {
	wsURL := os.Getenv("FAMCLAW_PLAYWRIGHT_WS")
	if wsURL == "" {
		t.Skip("FAMCLAW_PLAYWRIGHT_WS not set — no Playwright server available")
	}

	// A page whose visible text changes after JavaScript runs. The plain HTTP
	// fetch sees "loading...", the browser renders "loaded-by-js".
	const preJS = "loading..."
	const postJS = "loaded-by-js"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body><div id="content">%s</div>`+
			`<script>document.getElementById('content').textContent='%s'</script>`+
			`</body></html>`, preJS, postJS)
	}))
	defer srv.Close()

	// hostAllowed mirrors the production gate: the test server is on the
	// loopback address.
	hostAllowed := func(h string) bool {
		return h == "127.0.0.1" || h == "localhost"
	}

	pool, err := browser.NewPool(context.Background(), browser.Config{
		Endpoint:    wsURL,
		IdleTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Skipf("browser.NewPool: %v", err)
	}
	defer pool.Close()

	a := &Agent{
		user: &config.UserConfig{Name: "it-user", Role: "parent"},
	}
	res, err := a.fetchWithBrowser(context.Background(), pool, srv.URL, hostAllowed)
	if err != nil {
		t.Fatalf("fetchWithBrowser: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if !strings.Contains(res.Text, postJS) {
		t.Errorf("expected JS-rendered text %q in result, got: %q", postJS, res.Text)
	}
	if strings.Contains(res.Text, preJS) {
		t.Errorf("result should not contain pre-JS text %q, got: %q", preJS, res.Text)
	}
}
