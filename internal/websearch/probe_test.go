package websearch

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProbeSearchEndpoint_JsonVsNonJsonVsStatus is the Slice 2c contract at
// the unit level: only a real JSON search payload passes; a 200 that is not
// JSON (SearXNG's default HTML answer) is ErrMalformed with an actionable
// "set format=json" hint; every other non-2xx maps to its class; a slow
// endpoint is ErrTimeout and is never reported as "unreachable" (the
// fc-websearch-honest-failure lesson).
func TestProbeSearchEndpoint_JsonVsNonJsonVsStatus(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		body          string
		contentType   string
		latency       time.Duration
		clientTimeout time.Duration
		wantErr       bool
		wantClass     error
		wantMsg       string // substring the error must contain
		noReachable   bool   // error must NOT contain "unreachable"
	}{
		{
			name: "real JSON search payload passes", status: 200,
			body: `{"query":"x","results":[]}`, contentType: "application/json",
			wantErr: false,
		},
		{
			name: "200 HTML (format=json off) is malformed with hint", status: 200,
			body: `<!doctype html><html><head>Search</head><body></body></html>`, contentType: "text/html",
			wantErr: true, wantClass: ErrMalformed, wantMsg: "format=json",
		},
		{
			name: "200 without content-type is malformed with hint", status: 200,
			body: `<html>hi</html>`, contentType: "",
			wantErr: true, wantClass: ErrMalformed, wantMsg: "format=json",
		},
		{
			name: "200 broken JSON is malformed", status: 200,
			body: `{nope`, contentType: "application/json",
			wantErr: true, wantClass: ErrMalformed,
		},
		{
			name: "200 JSON without results key is malformed", status: 200,
			body: `{"query":"x"}`, contentType: "application/json",
			wantErr: true, wantClass: ErrMalformed, wantMsg: `"results"`,
		},
		{
			name: "401 is auth/config", status: 401,
			body: `unauthorized`, contentType: "text/plain",
			wantErr: true, wantClass: ErrAuthConfig,
		},
		{
			name: "403 is auth/config", status: 403,
			body: `forbidden`, contentType: "text/plain",
			wantErr: true, wantClass: ErrAuthConfig,
		},
		{
			name: "429 is rate limited", status: 429,
			body: `slow down`, contentType: "text/plain",
			wantErr: true, wantClass: ErrRateLimited,
		},
		{
			name: "500 is provider 5xx", status: 500,
			body: `boom`, contentType: "text/plain",
			wantErr: true, wantClass: ErrProvider5xx,
		},
		{
			name: "502 is provider 5xx", status: 502,
			body: ``, contentType: "text/plain",
			wantErr: true, wantClass: ErrProvider5xx,
		},
		{
			name: "404 is the undetermined umbrella", status: 404,
			body: `not found`, contentType: "text/plain",
			wantErr: true, wantClass: ErrUnavailable,
		},
		{
			name: "slow endpoint is a timeout, never unreachable", status: 200,
			body: `{"results":[]}`, contentType: "application/json",
			latency: 400 * time.Millisecond, clientTimeout: 100 * time.Millisecond,
			wantErr: true, wantClass: ErrTimeout, wantMsg: "timed out", noReachable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.latency > 0 {
					time.Sleep(tc.latency)
				}
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(ts.Close)

			err := ProbeSearchEndpoint(ts.URL, tc.clientTimeout)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("probe failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if tc.wantClass != nil && !errors.Is(err, tc.wantClass) {
				t.Errorf("errors.Is(%v) = false, got %v", tc.wantClass, err)
			}
			// Every class still wraps the umbrella.
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("errors.Is(ErrUnavailable) = false, got %v", err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
			if tc.noReachable && strings.Contains(err.Error(), "unreachable") {
				t.Errorf("a timeout must not be reported as %q, got %q", "unreachable", err.Error())
			}
		})
	}
}

// TestProbeSearchEndpoint_Refused: a refused connection is ErrConnection with
// the honest "could not connect" wording — never "unreachable" (which would
// conflate it with a live-but-erroring backend).
func TestProbeSearchEndpoint_Refused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	err = ProbeSearchEndpoint("http://"+addr, 2*time.Second)
	if err == nil {
		t.Fatal("expected an error for a refused connection, got nil")
	}
	if !errors.Is(err, ErrConnection) {
		t.Errorf("errors.Is(ErrConnection) = false, got %v", err)
	}
	if !strings.Contains(err.Error(), "could not connect") {
		t.Errorf("expected the honest 'could not connect' wording, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Errorf("a refused connection must not be called 'unreachable', got %q", err.Error())
	}
}

// TestProbeSearchEndpoint_EmptyEndpoint: an empty endpoint is a configuration
// message, not a network error.
func TestProbeSearchEndpoint_EmptyEndpoint(t *testing.T) {
	err := ProbeSearchEndpoint("", 2*time.Second)
	if err == nil {
		t.Fatal("expected an error for an empty endpoint")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected a configuration message, got %q", err.Error())
	}
}

// TestProbeSearchEndpoint_NoHost: a scheme-only endpoint has no host to dial.
func TestProbeSearchEndpoint_NoHost(t *testing.T) {
	err := ProbeSearchEndpoint("https://", 2*time.Second)
	if err == nil {
		t.Fatal("expected an error for a hostless endpoint")
	}
	if !strings.Contains(err.Error(), "must include a host") {
		t.Errorf("expected the hostless-endpoint message, got %q", err.Error())
	}
}

// TestProbeSearchEndpoint_PathJoining: an endpoint that already carries the
// full search path is probed at that exact path (no second /search appended);
// a bare host gets /search appended.
func TestProbeSearchEndpoint_PathJoining(t *testing.T) {
	t.Run("bare host gets /search", func(t *testing.T) {
		var gotPath string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"results":[]}`))
		}))
		t.Cleanup(ts.Close)
		if err := ProbeSearchEndpoint(ts.URL, 2*time.Second); err != nil {
			t.Fatalf("probe failed: %v", err)
		}
		if gotPath != "/search" {
			t.Errorf("path = %q, want /search", gotPath)
		}
	})

	t.Run("full search path is not doubled", func(t *testing.T) {
		var gotPath string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"results":[]}`))
		}))
		t.Cleanup(ts.Close)
		if err := ProbeSearchEndpoint(ts.URL+"/search", 2*time.Second); err != nil {
			t.Fatalf("probe failed: %v", err)
		}
		if gotPath != "/search" {
			t.Errorf("path = %q, want /search (no doubling)", gotPath)
		}
	})

	t.Run("sub-path endpoint joins under it", func(t *testing.T) {
		var gotPath string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"results":[]}`))
		}))
		t.Cleanup(ts.Close)
		if err := ProbeSearchEndpoint(ts.URL+"/searx", 2*time.Second); err != nil {
			t.Fatalf("probe failed: %v", err)
		}
		if gotPath != "/searx/search" {
			t.Errorf("path = %q, want /searx/search", gotPath)
		}
	})
}

// TestProbeSearchEndpoint_StripsUserinfo: credentials in the endpoint URL are
// stripped before the request (never sent, never echoed into the error).
func TestProbeSearchEndpoint_StripsUserinfo(t *testing.T) {
	seenUser := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := r.URL.User; u != nil {
			seenUser = u.String()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(ts.Close)

	err := ProbeSearchEndpoint("http://probe-user:probe-pass@"+strings.TrimPrefix(ts.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if seenUser != "" {
		t.Errorf("credentials were sent to the server: %q", seenUser)
	}
}
