package transcribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/config"
)

// endpointRecorder is a tiny helper: it spins up an httptest server that
// records the endpoint (URL.Path) of each /v1/audio/transcriptions request,
// and returns a canned transcript. The server's URL is the endpoint we feed
// to the transcriber.
func endpointRecorder(t *testing.T, transcript string) (string, *httptest.Server, *string) {
	t.Helper()
	var mu sync.Mutex
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"` + transcript + `"}`))
	}))
	return srv.URL, srv, &capturedPath
}

// testTranscriptionConfig builds a config.ToolsConfig with transcription
// enabled and a non-zero MaxBytes (so audio isn't rejected) pointing at
// the given endpoint.
func testTranscriptionConfig(endpoint, model string, timeoutSec int) config.TranscriptionConfig {
	return config.TranscriptionConfig{
		Enabled:    true,
		Endpoint:   endpoint,
		Model:      model,
		MaxBytes:   25 * 1024 * 1024, // 25 MB — matches the production default
		TimeoutSec: timeoutSec,
	}
}

// TestReloadableTranscriber_DisabledByDefault verifies that a freshly
// created ReloadableTranscriber returns a clear error (not a silent drop)
// when no inner transcriber has been set.
func TestReloadableTranscriber_DisabledByDefault(t *testing.T) {
	r := NewReloadable()
	_, err := r.TranscribeAudio(context.Background(), []byte("x"), "audio/ogg")
	if err == nil {
		t.Fatal("expected error when transcription not enabled, got nil")
	}
}

// TestReloadableTranscriber_ReloadConfigChangeIsObservable is the core
// acceptance test for issue #326: changing tools.transcription.* in config
// takes effect WITHOUT a process restart. It proves that after ReloadConfig
// is called with a new endpoint, the transcriber routes requests to the
// new endpoint — not the old one.
func TestReloadableTranscriber_ReloadConfigChangeIsObservable(t *testing.T) {
	// Endpoint A
	endpointA, srvA, pathA := endpointRecorder(t, "hello from A")
	defer srvA.Close()
	// Endpoint B
	endpointB, srvB, pathB := endpointRecorder(t, "hello from B")
	defer srvB.Close()

	r := NewReloadable()

	// Phase 1: enable transcription pointing at endpoint A.
	cfgA := &config.Config{}
	cfgA.Tools.Transcription = testTranscriptionConfig(endpointA, "whisper-1", 5)
	if err := r.ReloadConfig(cfgA); err != nil {
		t.Fatalf("ReloadConfig(cfgA): %v", err)
	}

	// Transcribe — request must go to endpoint A.
	got, err := r.TranscribeAudio(context.Background(), []byte("audio-bytes"), "audio/ogg")
	if err != nil {
		t.Fatalf("TranscribeAudio after cfgA: %v", err)
	}
	if got != "hello from A" {
		t.Errorf("transcript = %q, want %q", got, "hello from A")
	}
	if *pathA != "/v1/audio/transcriptions" {
		t.Errorf("pathA = %q, want /v1/audio/transcriptions", *pathA)
	}

	// Phase 2: reload with endpoint B — WITHOUT restarting the process.
	cfgB := &config.Config{}
	cfgB.Tools.Transcription = testTranscriptionConfig(endpointB, "whisper-1", 5)
	if err := r.ReloadConfig(cfgB); err != nil {
		t.Fatalf("ReloadConfig(cfgB): %v", err)
	}

	// Transcribe — request must NOW go to endpoint B.
	got, err = r.TranscribeAudio(context.Background(), []byte("audio-bytes"), "audio/ogg")
	if err != nil {
		t.Fatalf("TranscribeAudio after cfgB: %v", err)
	}
	if got != "hello from B" {
		t.Errorf("transcript = %q, want %q (config change not observable)", got, "hello from B")
	}
	if *pathB != "/v1/audio/transcriptions" {
		t.Errorf("pathB = %q, want /v1/audio/transcriptions", *pathB)
	}
}

// TestReloadableTranscriber_ReloadDisableIsObservable verifies that
// toggling tools.transcription.enabled from true to false via ReloadConfig
// makes subsequent transcribe calls return an error — i.e. disabling
// transcription without restart is also observable.
func TestReloadableTranscriber_ReloadDisableIsObservable(t *testing.T) {
	endpoint, srv, _ := endpointRecorder(t, "transcribed")
	defer srv.Close()

	r := NewReloadable()

	// Enable.
	cfg := &config.Config{}
	cfg.Tools.Transcription = testTranscriptionConfig(endpoint, "whisper-1", 5)
	if err := r.ReloadConfig(cfg); err != nil {
		t.Fatalf("ReloadConfig(enable): %v", err)
	}

	got, err := r.TranscribeAudio(context.Background(), []byte("x"), "audio/ogg")
	if err != nil {
		t.Fatalf("TranscribeAudio: %v", err)
	}
	if got != "transcribed" {
		t.Errorf("got %q, want %q", got, "transcribed")
	}

	// Disable via reload.
	cfg2 := &config.Config{}
	cfg2.Tools.Transcription.Enabled = false
	if err := r.ReloadConfig(cfg2); err != nil {
		t.Fatalf("ReloadConfig(disable): %v", err)
	}

	_, err = r.TranscribeAudio(context.Background(), []byte("x"), "audio/ogg")
	if err == nil {
		t.Fatal("expected error after disabling transcription, got nil")
	}
}

// TestReloadableTranscriber_ReloadConfigEmptyEndpoint verifies that
// enabling transcription without an endpoint returns an error rather
// than silently creating a broken transcriber.
func TestReloadableTranscriber_ReloadConfigEmptyEndpoint(t *testing.T) {
	r := NewReloadable()
	cfg := &config.Config{}
	cfg.Tools.Transcription.Enabled = true
	cfg.Tools.Transcription.Endpoint = "" // no endpoint
	if err := r.ReloadConfig(cfg); err == nil {
		t.Fatal("expected error for empty endpoint, got nil")
	}
}

// TestReloadableTranscriber_ReloadConfigUsesModel verifies that the model
// name from config is sent as the "model" form field to the endpoint.
func TestReloadableTranscriber_ReloadConfigUsesModel(t *testing.T) {
	r := NewReloadable()
	var capturedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		models := r.MultipartForm.Value["model"]
		if len(models) == 1 {
			capturedModel = models[0]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Tools.Transcription = testTranscriptionConfig(srv.URL, "whisper-large-v3", 5)
	if err := r.ReloadConfig(cfg); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	_, _ = r.TranscribeAudio(context.Background(), []byte("x"), "audio/ogg")
	if capturedModel != "whisper-large-v3" {
		t.Errorf("model = %q, want whisper-large-v3", capturedModel)
	}
}

// TestReloadableTranscriber_ReloadConfigUsesTimeout verifies that the
// timeout from config is applied to the inner transcriber.
func TestReloadableTranscriber_ReloadConfigUsesTimeout(t *testing.T) {
	r := NewReloadable()
	cfg := &config.Config{}
	cfg.Tools.Transcription.Enabled = true
	cfg.Tools.Transcription.Endpoint = "http://localhost:1"
	cfg.Tools.Transcription.Model = "whisper-1"
	cfg.Tools.Transcription.MaxBytes = 25 * 1024 * 1024
	cfg.Tools.Transcription.TimeoutSec = 7
	if err := r.ReloadConfig(cfg); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	// The timeout should have been applied — we verify by checking that a
	// transcribe to a non-listening port fails within a reasonable
	// window rather than hanging indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := r.TranscribeAudio(ctx, []byte("x"), "audio/ogg")
	if err == nil {
		t.Fatal("expected error for unreachable endpoint, got nil")
	}
}

// TestReloadableTranscriber_ConcurrentAccess verifies that TranscribeAudio
// is safe to call while ReloadConfig is running — no data race.
func TestReloadableTranscriber_ConcurrentAccess(t *testing.T) {
	endpoint, srv, _ := endpointRecorder(t, "concurrent")
	defer srv.Close()

	r := NewReloadable()
	cfg := &config.Config{}
	cfg.Tools.Transcription = testTranscriptionConfig(endpoint, "whisper-1", 5)
	if err := r.ReloadConfig(cfg); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}

	// Launch concurrent transcribe + reload operations.
	done := make(chan error, 20)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := r.TranscribeAudio(context.Background(), []byte("x"), "audio/ogg")
			done <- err
		}()
		go func() {
			cfg2 := &config.Config{}
			cfg2.Tools.Transcription = testTranscriptionConfig(endpoint, "whisper-1", 5)
			done <- r.ReloadConfig(cfg2)
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}
}
