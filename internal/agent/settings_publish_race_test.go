package agent

// #423 round 19 — committed -race regression for the residual
// settings-publish races found by the exact-head review of 51d2346
// (t_4bda885d, R1/R2). Derived from the reviewer's untracked probes;
// keep RED-until-fixed under -race:
//
//   - R1 (was FATAL): handleWebFetch re-read
//     a.cfg.Tools.WebFetch.FallbackMinTextLength / .FallbackToBrowser
//     WITHOUT the owner lock (short-text browser-fallback branch), racing
//     the settings POST whole-struct publish
//     (internal/web/settings.go: s.cfg.Tools.WebFetch = newWebFetch under
//     M). Fixed by using the local cfg snapshot taken under the owner's
//     RLock at the top of handleWebFetch.
//   - R2 (was MAJOR): handleWebSearch read
//     cfg := a.cfg.Tools.WebSearch unguarded, racing the settings
//     save-failure rollback whole-Tools assignment
//     (s.cfg.Tools = snapTools under M). Fixed by folding the WebSearch
//     copy into the owner RLock span.
//
// Run under -race (CGO_ENABLED=1 go test -race ./internal/agent/...):
// these pair the REAL production reader paths against the exact
// settings writer shapes.

import (
	"context"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/webfetch"
)

// TestSettingsPublishVsWebFetchFallbackRead pins R1: the real
// handleWebFetch short-text path (reader) vs an M-guarded
// whole-WebFetch publish, the exact settings POST writer shape
// (settings.go: owner.Lock(); newWebFetch := s.cfg.Tools.WebFetch; ...;
// s.cfg.Tools.WebFetch = newWebFetch; owner.Unlock()).
func TestSettingsPublishVsWebFetchFallbackRead(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.WebFetch.Enabled = true
	cfg.Tools.WebFetch.FallbackToBrowser = true
	cfg.Tools.WebFetch.FallbackMinTextLength = 10

	a, err := NewAgent(&config.UserConfig{Name: "settings-race", Role: "parent"}, cfg, nil, nil, nil, nil, AgentDeps{})
	if err != nil {
		t.Fatal(err)
	}
	// Stub the fetcher so handleWebFetch reaches the fallback-field reads
	// (the short-text browser-fallback branch) without any real network: a
	// 2-byte body is shorter than FallbackMinTextLength=10, so the branch
	// evaluates. NewAgent stores a nil *browser.Pool into the
	// BrowserFetcher interface field, which is a TYPED NIL (non-nil
	// interface) and would panic inside fetchWithBrowser. Zero the
	// interface so the short-text branch takes the documented no-pool
	// else path instead.
	a.browserPool = nil
	a.webFetcher = func(ctx context.Context, rawURL string, opts webfetch.Options) (*webfetch.Result, error) {
		return &webfetch.Result{
			URL:         rawURL,
			StatusCode:  200,
			ContentType: "text/plain",
			Bytes:       2,
			Truncated:   false,
			Text:        "hi",
		}, nil
	}

	var wg sync.WaitGroup
	const n = 200

	// Reader goroutines: the real handleWebFetch path.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.handleWebFetch(context.Background(), map[string]any{"url": "http://127.0.0.1:9/settings-race"}); err != nil {
				t.Errorf("handleWebFetch: %v", err)
			}
		}()
	}

	// Writer goroutines: the exact settings publish span — owner-guarded
	// whole-struct assignment (the struct copy at newWebFetch := ...
	// carries every field, and the assignment writes them all back).
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := cfg.WebFetchPolicyOwner()
			owner.Lock()
			newWebFetch := cfg.Tools.WebFetch
			_ = newWebFetch
			cfg.Tools.WebFetch = newWebFetch
			owner.Unlock()
		}()
	}
	wg.Wait()
}

// TestSettingsRollbackVsWebSearchRead pins R2: the real handleWebSearch
// config-read path (reader) vs the settings POST save-failure rollback,
// which assigns the WHOLE Tools struct under M (settings.go:
// snapTools := s.cfg.Tools; ...; s.cfg.Tools = snapTools). The WebSearch
// struct lives inside Tools, so every handleWebSearch invocation reads
// memory the rollback writes; with no owner-guarded snapshot on the
// reader side, -race flags the pair.
func TestSettingsRollbackVsWebSearchRead(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.WebSearch.Enabled = true
	// Leave the SearXNG endpoint unset: searxngProvider.Search fails
	// locally with "endpoint not configured" — no network, no transient
	// retry — after the production config read has happened. That read is
	// the site pinned here.
	a, err := NewAgent(&config.UserConfig{Name: "settings-rollback", Role: "parent"}, cfg, nil, nil, nil, nil, AgentDeps{})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const n = 200

	// Reader: the real handleWebSearch path, looped for density.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = a.handleWebSearch(context.Background(), map[string]any{"query": "settings-rollback"})
			}
		}()
	}

	// Writer: the settings rollback shape (settings.go) —
	// owner-guarded whole-Tools assignment.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := cfg.WebFetchPolicyOwner()
			owner.Lock()
			snapTools := cfg.Tools
			cfg.Tools = snapTools
			owner.Unlock()
		}()
	}
	wg.Wait()
}
