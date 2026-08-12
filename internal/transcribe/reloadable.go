package transcribe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/config"
)

// ReloadableTranscriber wraps a Transcriber behind a read-write mutex so
// that the underlying transcriber can be atomically rebuilt at runtime when
// the tools.transcription.* config block changes — without a container restart.
//
// main.go passes the ReloadableTranscriber directly into agent.AgentDeps.Transcriber
// (it satisfies the Transcriber interface). On config reload the registry
// calls ReloadConfig, which swaps the inner transcriber; the next Chat call
// observes the new one. This is what turns the previous "lying success"
// — where enabling tools.transcription in config did nothing until restart —
// into a real, observable reload.
type ReloadableTranscriber struct {
	mu    sync.RWMutex
	inner Transcriber
}

// NewReloadable creates a ReloadableTranscriber with no inner transcriber.
// Call ReloadConfig with an initial config to populate it.
func NewReloadable() *ReloadableTranscriber {
	return &ReloadableTranscriber{}
}

// Set replaces the inner transcriber. Used during initial boot and reload.
func (r *ReloadableTranscriber) Set(t Transcriber) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inner = t
}

// TranscribeAudio delegates to the current inner transcriber. Returns a
// clear error when no transcriber has been configured (transcription
// disabled) — never a silent drop.
func (r *ReloadableTranscriber) TranscribeAudio(ctx context.Context, audioData []byte, mimeType string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.inner == nil {
		return "", fmt.Errorf("transcribe: transcription is not enabled")
	}
	return r.inner.TranscribeAudio(ctx, audioData, mimeType)
}

// ReloadConfig rebuilds the inner transcriber from the config. If
// transcription is disabled (or the endpoint is empty), the inner
// transcriber is set to nil — subsequent calls return a clear error
// instead of being silently dropped.
//
// This method satisfies the reload.Reloader interface.
func (r *ReloadableTranscriber) ReloadConfig(cfg *config.Config) error {
	if !cfg.Tools.Transcription.Enabled {
		r.Set(nil)
		return nil
	}
	if cfg.Tools.Transcription.Endpoint == "" {
		return fmt.Errorf("transcription enabled but endpoint is empty")
	}
	timeout := time.Duration(cfg.Tools.Transcription.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second // sane default
	}
	r.Set(New(
		cfg.Tools.Transcription.Endpoint,
		cfg.Tools.Transcription.Model,
		cfg.Tools.Transcription.MaxBytes,
		timeout,
	))
	return nil
}
