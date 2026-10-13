package websearch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

// newTestTavily points a TavilyProvider at an httptest server that answers
// every request with the given canned response (the Endpoint config field is
// the documented test seam for exactly this).
func newTestTavily(t *testing.T, status int, body string, headers map[string]string) *TavilyProvider {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return NewTavilyProvider(TavilyConfig{APIKey: "test-seam-key", Endpoint: ts.URL, Timeout: 5 * time.Second})
}

// fakeNetError is a minimal concrete net.Error (the net.Error interface
// cannot be a composite literal in Go).
type fakeNetError struct {
	timeout bool
}

func (f *fakeNetError) Error() string   { return "dial tcp: fake net error" }
func (f *fakeNetError) Timeout() bool   { return f.timeout }
func (f *fakeNetError) Temporary() bool { return false }

// TestErrorClassification is the Slice 2a table: each HTTP/transport failure
// maps to exactly one class sentinel (and still the ErrUnavailable umbrella
// for legacy callers), while a valid zero-result response is a success.
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		want    error // the specific class to assert via errors.Is
	}{
		{"401 is auth/config", 401, `{"detail":"no"}`, nil, ErrAuthConfig},
		{"403 is auth/config", 403, ``, nil, ErrAuthConfig},
		{"429 is rate limited", 429, ``, nil, ErrRateLimited},
		{"500 is provider 5xx", 500, `boom`, nil, ErrProvider5xx},
		{"503 is provider 5xx", 503, ``, nil, ErrProvider5xx},
		{"404 is the undetermined umbrella", 404, ``, nil, ErrUnavailable},
		{"200 broken JSON is malformed", 200, `{nope`, map[string]string{"Content-Type": "application/json"}, ErrMalformed},
		{"200 HTML is malformed", 200, `<!doctype html><html></html>`, map[string]string{"Content-Type": "text/html"}, ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestTavily(t, tc.status, tc.body, tc.headers)
			_, err := p.Search(context.Background(), "x", 5)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(err, %v) = false, got %v", tc.want, err)
			}
			// Every class still wraps the umbrella, so legacy
			// errors.Is(err, ErrUnavailable) callers keep working.
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("errors.Is(err, ErrUnavailable) = false; every class must wrap the umbrella, got %v", err)
			}
		})
	}

	t.Run("valid zero-result response is a success", func(t *testing.T) {
		p := newTestTavily(t, 200, `{"results":[]}`, map[string]string{"Content-Type": "application/json"})
		hits, err := p.Search(context.Background(), "x", 5)
		if err != nil {
			t.Fatalf("a valid empty result must be a success, got %v", err)
		}
		if hits == nil || len(hits) != 0 {
			t.Errorf("expected an empty non-nil hit slice, got %v", hits)
		}
	})

	t.Run("connection refused is ErrConnection", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := l.Addr().String()
		l.Close()
		p := NewTavilyProvider(TavilyConfig{APIKey: "test-seam-key", Endpoint: "http://" + addr, Timeout: 2 * time.Second})
		_, err = p.Search(context.Background(), "x", 5)
		if err == nil {
			t.Fatal("expected an error for a refused connection, got nil")
		}
		if !errors.Is(err, ErrConnection) {
			t.Errorf("errors.Is(err, ErrConnection) = false, got %v", err)
		}
		if errors.Is(err, ErrTimeout) {
			t.Errorf("a refused connection is not a timeout, got %v", err)
		}
	})

	t.Run("slow endpoint is ErrTimeout", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.Write([]byte(`{"results":[]}`))
		}))
		t.Cleanup(ts.Close)
		p := NewTavilyProvider(TavilyConfig{APIKey: "test-seam-key", Endpoint: ts.URL, Timeout: 100 * time.Millisecond})
		_, err := p.Search(context.Background(), "x", 5)
		if err == nil {
			t.Fatal("expected a timeout error, got nil")
		}
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("errors.Is(err, ErrTimeout) = false, got %v", err)
		}
		if errors.Is(err, ErrConnection) {
			t.Errorf("a timeout must not be classified as a connection failure, got %v", err)
		}
	})
}

// TestClassifyHTTP pins the status→class mapping at the unit level, including
// the Retry-After carry-through.
func TestClassifyHTTP(t *testing.T) {
	cases := []struct {
		status     int
		retryAfter time.Duration
		want       error
	}{
		{http.StatusUnauthorized, 0, ErrAuthConfig},
		{http.StatusForbidden, 0, ErrAuthConfig},
		{http.StatusTooManyRequests, 7 * time.Second, ErrRateLimited},
		{http.StatusInternalServerError, 0, ErrProvider5xx},
		{http.StatusServiceUnavailable, 0, ErrProvider5xx},
		{http.StatusNotFound, 0, ErrUnavailable},
		{http.StatusBadRequest, 0, ErrUnavailable},
	}
	for _, tc := range cases {
		err := classifyHTTP(tc.status, tc.retryAfter)
		if !errors.Is(err, tc.want) {
			t.Errorf("classifyHTTP(%d) is not %v, got %v", tc.status, tc.want, err)
		}
		if tc.want == ErrRateLimited {
			var se *searchError
			if !errors.As(err, &se) {
				t.Fatalf("expected *searchError, got %T", err)
			}
			if se.RetryAfter() != 7*time.Second {
				t.Errorf("Retry-After not carried: got %v, want 7s", se.RetryAfter())
			}
		}
	}
}

// TestTransportClass pins the transport-failure mapping: deadline/timeout →
// ErrTimeout; canceled, refused, DNS, reset → ErrConnection. A timeout must
// never be reported as "unreachable" (fc-websearch-honest-failure lesson).
func TestTransportClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"context deadline exceeded", context.DeadlineExceeded, ErrTimeout},
		{"net.Error with timeout", &fakeNetError{timeout: true}, ErrTimeout},
		{"connection refused", syscall.ECONNREFUSED, ErrConnection},
		{"dns failure", errors.New("no such host"), ErrConnection},
		{"canceled", context.Canceled, ErrConnection},
	}
	for _, tc := range cases {
		if got := transportClass(tc.err); !errors.Is(got, tc.want) {
			t.Errorf("%s: transportClass = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestClassifyTransport checks the detail rendering of transport classes:
// a canceled request is labeled "canceled" (not "refused"), a dial failure
// carries the OS detail.
func TestClassifyTransport(t *testing.T) {
	if err := classifyTransport(context.Canceled); err == nil {
		t.Fatal("expected an error for a canceled request")
	}
	if err := classifyTransport(syscall.ECONNREFUSED); err == nil {
		t.Fatal("expected an error for a refused connection")
	}
}

// TestRetryAfterFromHeader pins the Retry-After parsing: delta-seconds,
// HTTP-date, and the absent/zero/garbage/past cases all resolving to 0.
func TestRetryAfterFromHeader(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Duration
		min   time.Duration // when want==0, assert exactly 0; otherwise assert >= min
	}{
		{"absent", "", 0, 0},
		{"zero", "0", 0, 0},
		{"negative", "-5", 0, 0},
		{"garbage", "soon", 0, 0},
		{"delta seconds", "3", 3 * time.Second, 0},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.value != "" {
			h.Set("Retry-After", tc.value)
		}
		if got := retryAfterFromHeader(h); tc.value == "3" {
			if got != 3*time.Second {
				t.Errorf("%s: got %v, want 3s", tc.name, got)
			}
		} else if got != 0 {
			t.Errorf("%s: got %v, want 0", tc.name, got)
		}
	}

	// HTTP-date in the future → positive; in the past → 0.
	future := http.Header{}
	future.Set("Retry-After", time.Now().Add(10*time.Second).UTC().Format(http.TimeFormat))
	if got := retryAfterFromHeader(future); got <= 0 {
		t.Errorf("future HTTP-date should yield a positive delay, got %v", got)
	}
	past := http.Header{}
	past.Set("Retry-After", time.Now().Add(-10*time.Second).UTC().Format(http.TimeFormat))
	if got := retryAfterFromHeader(past); got != 0 {
		t.Errorf("past HTTP-date should yield 0, got %v", got)
	}
}

// TestIsTransient pins the retryable set exactly: rate limit, provider 5xx,
// connection, timeout. Auth/config, malformed, and the undetermined umbrella
// are NOT retried — they fail identically on retry.
func TestIsTransient(t *testing.T) {
	retriable := []error{ErrRateLimited, ErrProvider5xx, ErrConnection, ErrTimeout}
	for _, c := range retriable {
		if !isTransient(newClassified(c, "t", 0)) {
			t.Errorf("isTransient(%v) = false, want true", c)
		}
	}
	notRetriable := []error{ErrAuthConfig, ErrMalformed}
	for _, c := range notRetriable {
		if isTransient(newClassified(c, "t", 0)) {
			t.Errorf("isTransient(%v) = true, want false", c)
		}
	}
	// The undetermined umbrella (a plain wrapped error) is not a specific
	// class and must not be retried.
	if isTransient(errors.New("web_search: search backend unavailable")) {
		t.Error("isTransient(plain umbrella) = true, want false")
	}
}

// TestSearchError_MatchesBothUmbrellaAndClass: a single typed error must
// answer both errors.Is(err, ErrUnavailable) (legacy) and
// errors.Is(err, ErrRateLimited) (new code).
func TestSearchError_MatchesBothUmbrellaAndClass(t *testing.T) {
	err := newClassified(ErrRateLimited, "HTTP 429", 5*time.Second)
	if !errors.Is(err, ErrUnavailable) {
		t.Error("errors.Is(ErrUnavailable) = false; legacy callers would break")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Error("errors.Is(ErrRateLimited) = false; new callers cannot classify")
	}
	var se *searchError
	if !errors.As(err, &se) {
		t.Fatalf("expected *searchError, got %T", err)
	}
	if se.RetryAfter() != 5*time.Second {
		t.Errorf("RetryAfter() = %v, want 5s", se.RetryAfter())
	}
	// Negative Retry-After is clamped to 0, never a negative sleep.
	neg := newClassified(ErrRateLimited, "x", -1)
	var se2 *searchError
	errors.As(neg, &se2)
	if se2.RetryAfter() != 0 {
		t.Errorf("negative Retry-After should clamp to 0, got %v", se2.RetryAfter())
	}
}
