package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/websearch"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type failingBrowserForAttribution struct{ err error }

func (f failingBrowserForAttribution) Exec(context.Context, browser.ExecInput) (string, error) {
	return "", f.err
}

func (f failingBrowserForAttribution) NavigateAndExtract(context.Context, string, string, browser.HostValidator) (string, int64, error) {
	return "", 0, f.err
}

func TestBrowserFailureAttributionAndPrivacy(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tool     string
		cause    error
		category string
	}{
		{"generic", "builtin__browser_navigate", errors.New("fake-private-error"), "failed"},
		{"cancelled", "builtin__browser_click", fmt.Errorf("fake-private-error: %w", context.Canceled), "cancelled"},
		{"timeout", "builtin__browser_fill", fmt.Errorf("fake-private-error: %w", context.DeadlineExceeded), "timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })
			a := &Agent{user: &config.UserConfig{Name: "example-user"}, browserPool: failingBrowserForAttribution{err: tt.cause}}
			out, err := a.makeBuiltinHandler()(context.Background(), tt.tool, map[string]any{"url": "https://example.invalid/fake-private-url", "value": "fake-private-argument"})
			if out != "" || !errors.Is(err, tt.cause) {
				t.Fatalf("result or original error changed: output=%q error=%v", out, err)
			}
			got := logs.String()
			for _, want := range []string{"example-user", tt.tool, tt.category} {
				if !strings.Contains(got, want) {
					t.Errorf("log missing %q: %q", want, got)
				}
			}
			for _, private := range []string{"fake-private-error", "fake-private-url", "fake-private-argument"} {
				if strings.Contains(got, private) {
					t.Errorf("log leaked private data %q", private)
				}
			}
		})
	}
}

func TestOutcomeWebFetchLogSafety(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"failure", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const marker = "synthetic-fetch-secret"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			a := &Agent{user: &config.UserConfig{Name: "test", Role: "parent"}, cfg: &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLAllowlist: []string{"example.test"}, MaxBytes: 1024, TimeoutSec: 1}}}, webFetcher: func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
				if tc.fail {
					return nil, errors.New(marker)
				}
				return &webfetch.Result{StatusCode: 200, Text: "safe-success"}, nil
			}}
			rawURL := "https://example.test/ordinary"
			if tc.fail {
				rawURL = "https://example.test/" + marker
			}
			out, err := a.handleWebFetch(context.Background(), map[string]any{"url": rawURL})
			if strings.Contains(logs.String(), marker) {
				t.Error("sensitive error or URL leaked to logs")
			}
			if tc.fail && err == nil {
				t.Error("error not preserved")
			}
			if !tc.fail && (err != nil || !strings.Contains(out, "safe-success")) {
				t.Error("successful result not preserved")
			}
		})
	}
}

func TestOutcomeSearchDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{"failure", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			// "fake-*" fixture: synthetic test marker, not a live secret. The
			// check-private-details guard (CREDENTIAL shape) only exempts the
			// documented placeholder conventions (fake-*/test-*/changeme/...),
			// so keep this value inside that allowlist.
			const secret = "fake-search-secret"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			chatter := &staleSearchChatter{script: []*llm.Message{{Role: "assistant", Content: "done"}}}
			turn := &agentcore.Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []agentcore.Tool{{Name: "builtin__web_search"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "search-id", Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "sample"}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
			stage := agentcore.NewStageToolLoop(agentcore.ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*agentcore.Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				if !tc.failed {
					return "sample result", nil
				}
				return webSearchError(fmt.Errorf("%s: %w", secret, websearch.ErrProvider5xx))
			}})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing tool result")
			}
			if tc.failed {
				if turn.ToolCalls[0].Error == nil {
					t.Error("classified search failure recorded as success")
				}
				if strings.Contains(logs.String(), secret) {
					t.Error("private search cause leaked to logs")
				}
				seenMarker := false
				for _, batch := range chatter.recorded {
					for _, m := range batch {
						if m.Role == "tool" && strings.Contains(m.Content, "web_search: search backend error") {
							seenMarker = true
						}
						if strings.Contains(m.Content, secret) {
							t.Error("private search cause leaked to model")
						}
					}
				}
				if !seenMarker {
					t.Error("classified marker missing from model transcript")
				}
			} else if turn.ToolCalls[0].Error != nil || turn.ToolCalls[0].Output != "sample result" {
				t.Error("successful search result not preserved")
			}
		})
	}
}

func TestHandleWebFetchFailureDiagnosticsArePrivate(t *testing.T) {
	if os.Getenv("FAMCLAW_FETCH_LOG_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHandleWebFetchFailureDiagnosticsArePrivate$")
		cmd.Env = append(os.Environ(), "FAMCLAW_FETCH_LOG_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated test: %v\n%s", err, output)
		}
		return
	}
	const secret = "fake-private-fetch-detail"
	for _, tc := range []struct {
		name   string
		result *webfetch.Result
		err    error
		want   string
	}{
		{"status", &webfetch.Result{StatusCode: 503}, errors.New(secret), "status=503"},
		{"canceled", nil, fmt.Errorf("%s: %w", secret, context.Canceled), "class=canceled"},
		{"deadline", nil, fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "class=deadline"},
		{"host", nil, webfetch.NewHostNotAllowedError(secret), "class=host_denied"},
		{"dns", nil, &net.DNSError{Err: secret, Name: secret}, "class=dns"},
		{"network", nil, &net.OpError{Op: "dial", Err: errors.New(secret)}, "class=network"},
		{"unknown", nil, errors.New(secret), "class=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			a := &Agent{user: &config.UserConfig{Name: secret}, cfg: &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLAllowlist: []string{"example.test"}, MaxBytes: 1024, TimeoutSec: 1}}}, webFetcher: func(context.Context, string, webfetch.Options) (*webfetch.Result, error) { return tc.result, tc.err }}
			_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.test/" + secret + "?q=" + secret})
			if err == nil {
				t.Fatal("expected fetch failure")
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("log %q does not contain %q", logs.String(), tc.want)
			}
			if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "example.test") {
				t.Errorf("private details in log %q", logs.String())
			}
		})
	}
}

func TestClassifiedWebSearchErrorFormatsSafelyAndPreservesCause(t *testing.T) {
	const secret = "fake-private-host.internal/path?token=fake-secret"
	host := webfetch.NewHostNotAllowedError(secret)
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"host", host}, {"configuration", websearch.ErrProviderNotConfigured},
		{"rate limit", websearch.ErrRateLimited}, {"server", websearch.ErrProvider5xx},
		{"timeout", websearch.ErrTimeout}, {"connection", websearch.ErrConnection},
		{"auth", websearch.ErrAuthConfig}, {"malformed", websearch.ErrMalformed}, {"unavailable", websearch.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%s: %w", secret, tc.cause)
			marker, err := webSearchError(cause)
			if marker == "" || err == nil {
				t.Fatal("classified failure missing marker or error")
			}
			for _, formatted := range []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)} {
				if strings.Contains(formatted, secret) || formatted != marker {
					t.Error("formatted error is not the fixed safe marker")
				}
			}
			if !errors.Is(err, cause) || !errors.Is(err, tc.cause) {
				t.Error("original cause or classification no longer unwraps")
			}
			if tc.name == "host" {
				var got *webfetch.HostNotAllowedError
				if !errors.As(err, &got) || got != host {
					t.Error("original typed cause not preserved")
				}
			}
		})
	}
	ordinary := errors.New(secret)
	if marker, err := webSearchError(ordinary); marker != "" || err != ordinary {
		t.Error("unclassified error identity changed")
	}
	if marker, err := webSearchError(nil); marker != "" || err != nil {
		t.Error("nil behavior changed")
	}
}

type lockedSearchLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedSearchLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedSearchLog) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}

func (b *lockedSearchLog) string() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestWebSearchErrorLogsSafeClass(t *testing.T) {
	var logs lockedSearchLog
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	const diagnostic = "diagnostic-canary.example.invalid/private-path"
	for _, tc := range []struct {
		name  string
		err   error
		label string
	}{
		{"rate limit", websearch.ErrRateLimited, "rate_limited"},
		{"server", websearch.ErrProvider5xx, "backend_error"},
		{"timeout", websearch.ErrTimeout, "timeout"},
		{"connection", websearch.ErrConnection, "connection"},
		{"auth", websearch.ErrAuthConfig, "auth_config"},
		{"malformed", websearch.ErrMalformed, "invalid_response"},
		{"unavailable", websearch.ErrUnavailable, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.reset()
			marker, err := webSearchError(fmt.Errorf("%s: %w", diagnostic, tc.err))
			if marker == "" || err == nil {
				t.Fatal("expected classified failure")
			}
			got := logs.string()
			if !strings.Contains(got, "[agent] web_search backend error: "+tc.label) {
				t.Errorf("missing safe class %q in log: %q", tc.label, got)
			}
			if strings.Contains(got, diagnostic) {
				t.Error("log exposed diagnostic text")
			}
		})
	}
}
