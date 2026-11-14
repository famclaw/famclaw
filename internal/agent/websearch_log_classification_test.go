package agent

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/famclaw/famclaw/internal/websearch"
)

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
	const secret = "private.example/path?token=secret"
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
			marker, err := webSearchError(fmt.Errorf("%s: %w", secret, tc.err))
			if marker == "" || err == nil {
				t.Fatal("expected classified failure")
			}
			got := logs.string()
			if !strings.Contains(got, "[agent] web_search backend error: "+tc.label) {
				t.Errorf("missing safe class %q in log: %q", tc.label, got)
			}
			if strings.Contains(got, secret) {
				t.Error("log exposed diagnostic text")
			}
		})
	}
}
