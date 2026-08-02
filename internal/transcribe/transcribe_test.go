package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribeAudio_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method %q", r.Method)
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("expected multipart/form-data content-type, got %q", ct)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		if r.MultipartForm == nil {
			t.Fatal("expected multipart form to be parsed")
		}
		models := r.MultipartForm.Value["model"]
		if len(models) != 1 || models[0] != "whisper-1" {
			t.Errorf("model = %v, want [whisper-1]", models)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(files))
		}
		if files[0].Filename != "voice.ogg" {
			t.Errorf("filename = %q, want voice.ogg", files[0].Filename)
		}
		json.NewEncoder(w).Encode(map[string]any{"text": "hello world"})
	}))
	defer srv.Close()

	tr := New(srv.URL, "whisper-1", 25*1024*1024, 30*time.Second)
	got, err := tr.TranscribeAudio(context.Background(), []byte("fake-ogg-bytes"), "audio/ogg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("transcript = %q, want %q", got, "hello world")
	}
}

func TestTranscribeAudio_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		serverCode int
		serverBody string
		mimeType   string
		want       string
		wantErr    bool
	}{
		{name: "ogg opus", serverBody: `{"text":"what time is it"}`, mimeType: "audio/ogg", want: "what time is it"},
		{name: "mp3", serverBody: `{"text":"turn left at the fork"}`, mimeType: "audio/mpeg", want: "turn left at the fork"},
		{name: "wav", serverBody: `{"text":"hello there"}`, mimeType: "audio/wav", want: "hello there"},
		{name: "empty transcript", serverBody: `{"text":""}`, mimeType: "audio/ogg", want: ""},
		{name: "server 500", serverCode: 500, serverBody: "server error body", mimeType: "audio/ogg", wantErr: true},
		{name: "server 413", serverCode: 413, serverBody: "too large", mimeType: "audio/ogg", wantErr: true},
		{name: "server returns invalid json", serverBody: "not-json", mimeType: "audio/ogg", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ct := r.Header.Get("Content-Type")
				if !strings.HasPrefix(ct, "multipart/form-data") {
					t.Errorf("expected multipart, got %q", ct)
				}
				_ = r.ParseMultipartForm(32 << 20)
				if tc.serverCode != 0 {
					w.WriteHeader(tc.serverCode)
					_, _ = w.Write([]byte(tc.serverBody))
					return
				}
				_, _ = w.Write([]byte(tc.serverBody))
			}))
			defer srv.Close()
			tr := New(srv.URL, "whisper-1", 25*1024*1024, 30*time.Second)
			got, err := tr.TranscribeAudio(context.Background(), []byte("audio-bytes"), tc.mimeType)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("transcript = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranscribeAudio_EmptyData(t *testing.T) {
	tr := New("http://localhost:9999", "whisper-1", 25*1024*1024, 30*time.Second)
	_, err := tr.TranscribeAudio(context.Background(), nil, "audio/ogg")
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestTranscribeAudio_TooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"text": "x"})
	}))
	defer srv.Close()
	tr := New(srv.URL, "whisper-1", 10, 30*time.Second) // 10 byte cap
	big := make([]byte, 11)
	_, err := tr.TranscribeAudio(context.Background(), big, "audio/ogg")
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
}

func TestTranscribeAudio_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(map[string]any{"text": "x"})
	}))
	defer srv.Close()
	tr := New(srv.URL, "whisper-1", 25*1024*1024, 30*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := tr.TranscribeAudio(ctx, []byte("data"), "audio/ogg")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestExtFromMIME(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"audio/mpeg", "mp3"},
		{"audio/mp3", "mp3"},
		{"audio/wav", "wav"},
		{"audio/wave", "wav"},
		{"audio/x-wav", "wav"},
		{"audio/ogg", "ogg"},
		{"application/ogg", "ogg"},
		{"audio/opus", "ogg"},
		{"audio/webm", "webm"},
		{"audio/mp4", "mp4"},
		{"audio/aac", "mp4"},
		{"audio/x-aac", "mp4"},
		{"audio/unknown", "unknown"},
		{"", "ogg"},
	}
	for _, tc := range tests {
		t.Run(tc.mime, func(t *testing.T) {
			got := extFromMIME(tc.mime)
			if got != tc.want {
				t.Errorf("extFromMIME(%q) = %q, want %q", tc.mime, got, tc.want)
			}
		})
	}
}

func TestExtFromMIME_NotFilenameSafe(t *testing.T) {
	// A MIME subtype with path-traversal chars must not yield a usable extension.
	if ext := extFromMIME("audio/../../etc/passwd"); ext != "ogg" {
		t.Errorf("expected safe fallback ogg, got %q", ext)
	}
}

func TestNew_EndpointTrim(t *testing.T) {
	tr := New("http://localhost:8092/", "whisper-1", 25*1024*1024, 30*time.Second)
	if tr.endpoint != "http://localhost:8092" {
		t.Errorf("endpoint not trimmed: %q", tr.endpoint)
	}
}
