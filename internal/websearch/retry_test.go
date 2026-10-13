package websearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeProvider is a scripted Provider for the retry-decorator tests: each
// call gets the attempt number (1-based) and returns whatever the script
// says.
type fakeProvider struct {
	calls    int
	behavior func(attempt int) ([]Hit, error)
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Search(_ context.Context, _ string, _ int) ([]Hit, error) {
	f.calls++
	return f.behavior(f.calls)
}

func okHits() []Hit {
	return []Hit{{Title: "ok", URL: "https://example.com", Content: "fine"}}
}

// transientErr builds a classified transient error with a 1 ms Retry-After so
// tests with a few attempts run in milliseconds instead of the 500 ms/1 s
// exponential backoff.
func transientErr(class error, detail string) error {
	return newClassified(class, detail, time.Millisecond)
}

// TestRetryingProvider_RetriesTransientThenSucceeds: two connection failures
// followed by a success must retry through and return the good result. The
// inner provider sees three attempts.
func TestRetryingProvider_RetriesTransientThenSucceeds(t *testing.T) {
	inner := &fakeProvider{behavior: func(attempt int) ([]Hit, error) {
		if attempt < 3 {
			return nil, transientErr(ErrConnection, fmt.Sprintf("dial attempt %d failed", attempt))
		}
		return okHits(), nil
	}}
	p := &retryingProvider{inner: inner, maxRetries: 2}
	hits, err := p.Search(context.Background(), "q", 3)
	if err != nil {
		t.Fatalf("expected success after transient retries, got %v", err)
	}
	if len(hits) != 1 || hits[0].URL != "https://example.com" {
		t.Errorf("expected the good result, got %+v", hits)
	}
	if inner.calls != 3 {
		t.Errorf("inner provider called %d times, want 3 (initial + 2 retries)", inner.calls)
	}
}

// TestRetryingProvider_NonTransientFailsFast: an auth failure must not be
// retried (a wrong key fails identically every time) — exactly one inner
// call, the auth class surfaced.
func TestRetryingProvider_NonTransientFailsFast(t *testing.T) {
	inner := &fakeProvider{behavior: func(int) ([]Hit, error) {
		return nil, newClassified(ErrAuthConfig, "401", 0)
	}}
	p := &retryingProvider{inner: inner, maxRetries: 3}
	_, err := p.Search(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrAuthConfig) {
		t.Errorf("expected ErrAuthConfig, got %v", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("auth errors must keep wrapping the umbrella, got %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("auth failure must not be retried: inner called %d times, want 1", inner.calls)
	}
}

// TestRetryingProvider_BudgetExhaustedReturnsLastClass: when the retry budget
// is spent, the LAST error (with its class and Retry-After) is returned
// unwrapped — the caller still sees the real 429, not a bare "gave up".
func TestRetryingProvider_BudgetExhaustedReturnsLastClass(t *testing.T) {
	inner := &fakeProvider{behavior: func(attempt int) ([]Hit, error) {
		return nil, newClassified(ErrRateLimited, fmt.Sprintf("HTTP 429 (attempt %d)", attempt), time.Millisecond)
	}}
	p := &retryingProvider{inner: inner, maxRetries: 2}
	start := time.Now()
	_, err := p.Search(context.Background(), "q", 3)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error after exhausting the retry budget")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited to survive, got %v", err)
	}
	var se *searchError
	if !errors.As(err, &se) {
		t.Fatalf("expected *searchError, got %T", err)
	}
	if se.RetryAfter() != time.Millisecond {
		t.Errorf("Retry-After must survive, got %v", se.RetryAfter())
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("the last 429 detail must be in the message, got %q", err.Error())
	}
	if inner.calls != 3 {
		t.Errorf("inner called %d times, want 3 (initial + 2 retries)", inner.calls)
	}
	if elapsed > 5*time.Second {
		t.Errorf("budget exhaustion took %v; the 1 ms Retry-After should keep this fast", elapsed)
	}
}

// TestRetryingProvider_HonorsCtxDeadline: when the remaining deadline is
// shorter than the backoff, the decorator must NOT sleep — it returns the
// last error immediately. With a 150 ms context and a 500 ms backoff this
// must finish well under 400 ms.
func TestRetryingProvider_HonorsCtxDeadline(t *testing.T) {
	inner := &fakeProvider{behavior: func(int) ([]Hit, error) {
		return nil, newClassified(ErrConnection, "refused", 0) // no Retry-After → 500 ms backoff
	}}
	p := &retryingProvider{inner: inner, maxRetries: 5}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Search(ctx, "q", 3)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the last error after the deadline")
	}
	if !errors.Is(err, ErrConnection) {
		t.Errorf("expected ErrConnection, got %v", err)
	}
	if elapsed >= 400*time.Millisecond {
		t.Errorf("returned after %v — the backoff must yield to the remaining deadline, not sleep the full 500 ms", elapsed)
	}
	if inner.calls != 1 {
		t.Errorf("inner called %d times, want 1 (deadline check happens before any second attempt)", inner.calls)
	}
}

// TestRetryingProvider_CancelMidBackoff: canceling during a backoff wake-up
// returns the last error immediately instead of proceeding to another
// attempt.
func TestRetryingProvider_CancelMidBackoff(t *testing.T) {
	inner := &fakeProvider{behavior: func(int) ([]Hit, error) {
		return nil, newClassified(ErrConnection, "refused", 0)
	}}
	p := &retryingProvider{inner: inner, maxRetries: 3}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		time.Sleep(60 * time.Millisecond) // inside the 500 ms backoff window
		cancel()
		time.Sleep(200 * time.Millisecond)
		close(done)
	}()
	start := time.Now()
	_, err := p.Search(ctx, "q", 3)
	elapsed := time.Since(start)
	<-done // let the canceller goroutine finish before the test ends

	if err == nil {
		t.Fatal("expected the last error after cancel")
	}
	if !errors.Is(err, ErrConnection) {
		t.Errorf("expected ErrConnection, got %v", err)
	}
	if elapsed >= 300*time.Millisecond {
		t.Errorf("cancel during backoff should return promptly, took %v", elapsed)
	}
}

// TestRetryingProvider_HonorsRetryAfter: a 429 with Retry-After: 150ms must
// delay at least that long before the next attempt (Retry-After beats the
// exponential schedule).
func TestRetryingProvider_HonorsRetryAfter(t *testing.T) {
	inner := &fakeProvider{behavior: func(attempt int) ([]Hit, error) {
		if attempt == 1 {
			return nil, newClassified(ErrRateLimited, "HTTP 429", 150*time.Millisecond)
		}
		return okHits(), nil
	}}
	p := &retryingProvider{inner: inner, maxRetries: 1}
	start := time.Now()
	hits, err := p.Search(context.Background(), "q", 3)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success on the second attempt, got %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected the good result, got %+v", hits)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("returned after %v — the 150 ms Retry-After was not honored", elapsed)
	}
}

// TestRetryDelay pins the exponential schedule, the 2 s cap, and Retry-After
// precedence (a server's delay beats the schedule; a tiny one does not).
func TestRetryDelay(t *testing.T) {
	ra := newClassified(ErrRateLimited, "x", 3*time.Second)
	smallRA := newClassified(ErrRateLimited, "x", 150*time.Millisecond)
	plain := newClassified(ErrConnection, "x", 0)

	cases := []struct {
		name    string
		err     error
		attempt int
		want    time.Duration
	}{
		{"first attempt base", plain, 0, 500 * time.Millisecond},
		{"second attempt", plain, 1, 1 * time.Second},
		{"third attempt", plain, 2, 2 * time.Second},
		{"fourth attempt capped at 2s", plain, 3, 2 * time.Second},
		{"fifth attempt still capped", plain, 4, 2 * time.Second},
		{"long Retry-After wins", ra, 0, 3 * time.Second},
		{"short Retry-After loses to backoff", smallRA, 1, 1 * time.Second},
		{"negative Retry-After clamped to 0", newClassified(ErrRateLimited, "x", -1), 0, 500 * time.Millisecond},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.err, tc.attempt); got != tc.want {
			t.Errorf("%s: retryDelay(%v, %d) = %v, want %v", tc.name, tc.err, tc.attempt, got, tc.want)
		}
	}
}
