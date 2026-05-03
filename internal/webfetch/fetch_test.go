package webfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// localOpts returns Options with AllowPrivateNetworks=true so tests can
// reach httptest servers on 127.0.0.1.
func localOpts(o Options) Options {
	o.AllowPrivateNetworks = true
	return o
}

func TestFetch(t *testing.T) {
	t.Run("HTML", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body><p>Hello</p><script>x()</script><p>World</p></body></html>"))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.StatusCode != 200 {
			t.Errorf("status: got %d", res.StatusCode)
		}
		if !strings.Contains(res.Text, "Hello") {
			t.Errorf("missing Hello: %q", res.Text)
		}
		if !strings.Contains(res.Text, "World") {
			t.Errorf("missing World: %q", res.Text)
		}
		if strings.Contains(res.Text, "x()") {
			t.Errorf("script leaked: %q", res.Text)
		}
		if res.Truncated {
			t.Errorf("should not be truncated")
		}
	})

	t.Run("PlainText", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("raw text"))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Text != "raw text" {
			t.Errorf("text: %q", res.Text)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Text != `{"ok":true}` {
			t.Errorf("text: %q", res.Text)
		}
	})

	t.Run("DisallowedType", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG"))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(err.Error(), "content type") {
			t.Errorf("err msg: %v", err)
		}
		if res == nil || res.StatusCode != 200 {
			t.Errorf("res: %+v", res)
		}
	})

	t.Run("Truncation", func(t *testing.T) {
		big := strings.Repeat("a", 1024*1024)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(big))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{MaxBytes: 1024}))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !res.Truncated {
			t.Errorf("expected truncated")
		}
		if int64(len(res.Text)) > 1024 {
			t.Errorf("text too big: %d", len(res.Text))
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("late"))
		}))
		defer srv.Close()
		_, err := Fetch(context.Background(), srv.URL, localOpts(Options{Timeout: 20 * time.Millisecond}))
		if err == nil {
			t.Fatalf("expected timeout error")
		}
	})

	t.Run("NonOKStatus", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(503)
			w.Write([]byte("nope"))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, localOpts(Options{}))
		if err == nil {
			t.Fatalf("expected error on 503")
		}
		if res == nil || res.StatusCode != 503 {
			t.Errorf("status: %+v", res)
		}
	})
}

func TestFetch_PrivateIPBlockedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("secret"))
	}))
	defer srv.Close()

	// AllowPrivateNetworks left at zero-value (false) — must reject.
	_, err := Fetch(context.Background(), srv.URL, Options{})
	if err == nil {
		t.Fatalf("expected blocked-IP error reaching loopback, got nil")
	}
	if !strings.Contains(err.Error(), "blocked IP") {
		t.Errorf("expected 'blocked IP' in error, got: %v", err)
	}
}

func TestFetch_RejectsNonHTTPScheme(t *testing.T) {
	tests := []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"gopher://example.com/x",
		"javascript:alert(1)",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := Fetch(context.Background(), raw, Options{})
			if err == nil {
				t.Fatalf("expected scheme error, got nil")
			}
			if !strings.Contains(err.Error(), "scheme") {
				t.Errorf("expected 'scheme' in error, got: %v", err)
			}
		})
	}
}

func TestFetch_HostValidatorBlocksRedirect(t *testing.T) {
	// Two servers: "evil" hosts the actual data; "good" 302s to evil.
	// HostValidator only allows the "good" host's port — the redirect
	// must be blocked.
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("exfil-payload"))
	}))
	defer evil.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusFound)
	}))
	defer good.Close()

	// Both hosts resolve to 127.0.0.1, but ports differ. The validator
	// pins the allowed *port* via the rawURL we give it (since httptest
	// returns "127.0.0.1:NNNN" all on loopback, we discriminate by full
	// host:port string at the validator level).
	allowed := good.URL
	calls := 0
	validator := func(host string) error {
		calls++
		// Accept both servers initially, but the redirect target's full
		// authority must be the good one. Use a closure that knows the
		// allowed authority. Since httptest servers share host (loopback)
		// but differ by port, we need to inspect via the original URL
		// passed to Fetch — we only allow exactly the "good" host:port.
		if host == "" {
			return fmt.Errorf("empty host")
		}
		// In this synthetic test both are 127.0.0.1, so we instead trust
		// the redirect-host check via URL on the Fetch side. We just
		// confirm the validator runs.
		return nil
	}

	res, err := Fetch(context.Background(), allowed, localOpts(Options{HostValidator: validator}))
	if err != nil {
		t.Fatalf("unexpected err with permissive validator: %v (res=%+v)", err, res)
	}
	if calls < 2 {
		t.Errorf("validator should run for initial + redirect (>=2), got %d", calls)
	}

	// Now a strict validator that denies the redirect target's host
	// pattern. Since both are 127.0.0.1 we use the URL.Hostname() — they
	// share it — so we instead simulate by denying on the second call.
	callCount := 0
	denyOnRedirect := func(host string) error {
		callCount++
		if callCount > 1 {
			return fmt.Errorf("host %q not in url_allowlist", host)
		}
		return nil
	}
	_, err = Fetch(context.Background(), allowed, localOpts(Options{HostValidator: denyOnRedirect}))
	if err == nil {
		t.Fatalf("expected redirect to be blocked")
	}
	if !strings.Contains(err.Error(), "url_allowlist") && !strings.Contains(err.Error(), "redirect") {
		t.Errorf("expected redirect/allowlist error, got: %v", err)
	}
}

func TestFetch_HostValidatorBlocksInitialURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	deny := func(host string) error { return fmt.Errorf("host %q not in url_allowlist", host) }
	_, err := Fetch(context.Background(), srv.URL, localOpts(Options{HostValidator: deny}))
	if err == nil {
		t.Fatalf("expected initial-host validation to fail")
	}
	if !strings.Contains(err.Error(), "url_allowlist") {
		t.Errorf("expected url_allowlist error, got: %v", err)
	}
}
