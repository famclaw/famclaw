package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/webfetch"
)

// Run the real handler with isolated logging so other tests cannot affect the
// captured output. No browser or network is used.
func TestWebFetchLogPrivacy(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		thin, fail, pool bool
		wantCalls        int
		marker           string
	}{
		{name: "direct_success", pool: true, marker: "status=200"},
		{name: "cache_failure", pool: true, marker: "cache spillover failed"},
		{name: "403_fallback_success", status: 403, pool: true, wantCalls: 1, marker: "HTTP 403"},
		{name: "429_fallback_success", status: 429, pool: true, wantCalls: 1, marker: "HTTP 429"},
		{name: "403_fallback_failure", status: 403, pool: true, fail: true, wantCalls: 1, marker: "HTTP 403"},
		{name: "429_fallback_failure", status: 429, pool: true, fail: true, wantCalls: 1, marker: "HTTP 429"},
		{name: "thin_fallback_success", thin: true, pool: true, wantCalls: 1, marker: "browser fallback"},
		{name: "thin_fallback_failure", thin: true, pool: true, fail: true, wantCalls: 1, marker: "browser fallback failed"},
		{name: "thin_missing_pool", thin: true, marker: "no browser pool"},
		{name: "direct_error", status: 503, pool: true, marker: "web_fetch failed"},
	}
	selected := os.Getenv("FAMCLAW_TEST_WEBFETCH_PRIVACY")
	if selected == "" {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestWebFetchLogPrivacy$")
				cmd.Env = append(os.Environ(), "FAMCLAW_TEST_WEBFETCH_PRIVACY="+tc.name)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("child failed: %v\n%s", err, out)
				}
			})
		}
		return
	}
	for _, tc := range cases {
		if tc.name != selected {
			continue
		}
		rawURL := "https://example.com/fake-path?token=fake-token"
		fake := &fakeBrowserPool{text: "fake browser rendered content", bytes: 128}
		if tc.fail {
			fake.navFn = func(context.Context, string, string, browser.HostValidator) (string, int64, error) {
				return "", 0, errors.New("fake-browser-error")
			}
		}
		var pool *fakeBrowserPool
		if tc.pool {
			pool = fake
		}
		a := newBotBlockAgent(tc.status, true, pool)
		a.user.Name = "fake-user"
		if tc.name == "cache_failure" {
			db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			cache, err := toolcache.New(toolcache.Config{DB: db.SQL(), CacheDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			a.cache = cache
		}
		a.webFetcher = func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
			if tc.status != 0 {
				return &webfetch.Result{URL: rawURL, StatusCode: tc.status}, fmt.Errorf("fake-fetch-error %s", rawURL)
			}
			text := "fake full direct response"
			if tc.name == "cache_failure" {
				text = strings.Repeat("x", 65536)
			}
			if tc.thin {
				text = "ok"
			}
			return &webfetch.Result{URL: rawURL, StatusCode: 200, Text: text}, nil
		}
		var captured bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&captured)
		defer log.SetOutput(previous)
		_, err := a.handleWebFetch(context.Background(), map[string]any{"url": rawURL})
		if tc.status == 503 || (tc.status != 0 && tc.fail) {
			if err == nil {
				t.Fatal("expected original error")
			}
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fake.calls) != tc.wantCalls {
			t.Fatalf("fallback calls=%d want=%d", len(fake.calls), tc.wantCalls)
		}
		if tc.wantCalls > 0 && (fake.calls[0].rawURL != rawURL || fake.calls[0].hostCheck == nil) {
			t.Fatal("fallback lost URL or host validator")
		}
		logs := captured.String()
		if !strings.Contains(logs, tc.marker) {
			t.Fatalf("missing diagnostic %q", tc.marker)
		}
		for _, secret := range []string{"fake-user", "fake-path", "fake-token", "fake-fetch-error", "fake-browser-error", rawURL} {
			if strings.Contains(logs, secret) {
				t.Fatalf("log exposed %q", secret)
			}
		}
		return
	}
	t.Fatal("unknown test case")
}
