package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// litellmCase describes one MergeSettings scenario. The handler sees the
// request plus a shared counter so retry behaviour is assertable.
type litellmCase struct {
	name       string
	baseSuffix string // appended to the test server URL ("", "/v1")
	apiKey     string
	handler    func(w http.ResponseWriter, r *http.Request, count *atomic.Int32)
	want       map[string]*bool
	wantErr    bool
	wantCount  int    // exact request count; 0 = don't check
	wantPaths  string // exact comma-joined path sequence; "" = don't check
}

func TestLiteLLMClientMergeSettings(t *testing.T) {
	// The live fleet gateway shape: /v1/model/info carries the full
	// litellm_params, including an explicit false for unflagged models.
	modelInfoBody := `{
		"object": "list",
		"data": [
			{"model_name": "fast", "litellm_params": {"model": "openai/qwen2.5-coder-7b", "merge_reasoning_content_in_choices": false}},
			{"model_name": "council", "litellm_params": {"model": "openai/gemma-4-26b", "merge_reasoning_content_in_choices": true}},
			{"model_name": "whisper", "litellm_params": {"model": "openai/whisper-1"}},
			{"model_name": ""}
		]
	}`
	modelListBody := `{
		"object": "list",
		"data": [
			{"id": "smart", "object": "model", "litellm_params": {"merge_reasoning_content_in_choices": false}},
			{"id": "council", "object": "model", "litellm_params": {"merge_reasoning_content_in_choices": true}}
		]
	}`

	yes, no := true, false

	cases := []litellmCase{
		{
			name: "model/info flags parsed",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				w.Write([]byte(modelInfoBody))
			},
			want: map[string]*bool{
				"fast":    &no,
				"council": &yes,
				"whisper": nil,
			},
		},
		{
			name:   "auth header sent when key set",
			apiKey: "sk-fleet",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				w.Write([]byte(`{"data":[]}`))
			},
			want:      map[string]*bool{},
			wantCount: 1,
			wantPaths: "/v1/model/info",
		},
		{
			name:       "base URL already ends in /v1",
			baseSuffix: "/v1",
			handler: func(w http.ResponseWriter, r *http.Request, count *atomic.Int32) {
				count.Add(1)
				if r.URL.Path == "/v1/model/info" {
					w.Write([]byte(modelInfoBody))
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			},
			want: map[string]*bool{
				"fast":    &no,
				"council": &yes,
				"whisper": nil,
			},
		},
		{
			name: "model/info 404 falls back to /v1/models",
			handler: func(w http.ResponseWriter, r *http.Request, count *atomic.Int32) {
				count.Add(1)
				switch r.URL.Path {
				case "/v1/model/info":
					http.Error(w, `{"detail":"Not Found"}`, http.StatusNotFound)
				case "/v1/models":
					w.Write([]byte(modelListBody))
				default:
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			},
			want:      map[string]*bool{"smart": &no, "council": &yes},
			wantCount: 2,
			wantPaths: "/v1/model/info,/v1/models",
		},
		{
			name: "both endpoints 404 is an error",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				http.Error(w, "nope", http.StatusNotFound)
			},
			wantErr:   true,
			wantCount: 2, // primary + fallback; 404 is not retried
		},
		{
			name: "401 is not retried and does not fall back",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			},
			wantErr:   true,
			wantCount: 1,
		},
		{
			name: "500 retried once then errors",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantErr:   true,
			wantCount: 2,
		},
		{
			name: "502 retried once then errors (all 5xx are retryable)",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				http.Error(w, "bad gateway", http.StatusBadGateway)
			},
			wantErr:   true,
			wantCount: 2,
		},
		{
			name: "500 then 200 succeeds on the retry",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				if count.Add(1) == 1 {
					http.Error(w, "boom", http.StatusServiceUnavailable)
					return
				}
				w.Write([]byte(`{"data":[{"model_name":"council","litellm_params":{"merge_reasoning_content_in_choices":true}}]}`))
			},
			want:      map[string]*bool{"council": &yes},
			wantCount: 2,
		},
		{
			name: "malformed JSON is an error",
			handler: func(w http.ResponseWriter, _ *http.Request, count *atomic.Int32) {
				count.Add(1)
				w.Write([]byte(`{"data": [oops`))
			},
			wantErr: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var counter atomic.Int32
			var paths []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				tt.handler(w, r, &counter)
			}))
			defer server.Close()

			c := NewLiteLLMClient(server.URL+tt.baseSuffix, tt.apiKey)
			got, err := c.MergeSettings(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got settings %v", got)
				}
			} else if err != nil {
				t.Fatalf("MergeSettings: %v", err)
			} else {
				for model, want := range tt.want {
					g, ok := got[model]
					if !ok {
						t.Errorf("model %q missing from result %v", model, got)
						continue
					}
					if want == nil {
						if g != nil {
							t.Errorf("model %q: want nil (heuristic), got %v", model, *g)
						}
					} else if g == nil || *g != *want {
						t.Errorf("model %q: want %v, got %v", model, *want, g)
					}
				}
				if len(got) != len(tt.want) {
					t.Errorf("result = %v, want exactly %v", got, tt.want)
				}
			}
			if tt.wantCount != 0 && int(counter.Load()) != tt.wantCount {
				t.Errorf("request count = %d, want %d", counter.Load(), tt.wantCount)
			}
			if tt.wantPaths != "" && strings.Join(paths, ",") != tt.wantPaths {
				t.Errorf("paths = %q, want %q", strings.Join(paths, ","), tt.wantPaths)
			}
		})
	}
}

func TestLiteLLMClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	c := NewLiteLLMClient(server.URL, "")
	c.http = &http.Client{Timeout: 50 * time.Millisecond}
	if _, err := c.MergeSettings(context.Background()); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestLiteLLMClientAuthHeader(t *testing.T) {
	var gotAuth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	c := NewLiteLLMClient(server.URL, "sk-test")
	if _, err := c.MergeSettings(context.Background()); err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	if got := gotAuth.Load().(string); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer sk-test")
	}
}
