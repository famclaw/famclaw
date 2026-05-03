package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetch(t *testing.T) {
	t.Run("HTML", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body><p>Hello</p><script>x()</script><p>World</p></body></html>"))
		}))
		defer srv.Close()
		res, err := Fetch(context.Background(), srv.URL, Options{})
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
		res, err := Fetch(context.Background(), srv.URL, Options{})
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
		res, err := Fetch(context.Background(), srv.URL, Options{})
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
		res, err := Fetch(context.Background(), srv.URL, Options{})
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
		res, err := Fetch(context.Background(), srv.URL, Options{MaxBytes: 1024})
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
		_, err := Fetch(context.Background(), srv.URL, Options{Timeout: 20 * time.Millisecond})
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
		res, err := Fetch(context.Background(), srv.URL, Options{})
		if err == nil {
			t.Fatalf("expected error on 503")
		}
		if res == nil || res.StatusCode != 503 {
			t.Errorf("status: %+v", res)
		}
	})
}
