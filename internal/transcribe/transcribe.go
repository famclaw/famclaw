// Package transcribe provides audio-to-text transcription for inbound
// voice messages. It calls an OpenAI-compatible /v1/audio/transcriptions
// endpoint (e.g. a local whisper.cpp server or a LiteLLM gateway).
package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Transcriber converts raw audio bytes into a transcript string.
// Implementations must not retain the audio data after returning.
type Transcriber interface {
	// TranscribeAudio transcribes the given audio data. mimeType is the
	// media type of the audio (e.g. "audio/ogg", "audio/mpeg") which is
	// used as a hint to the transcription service. Returns the transcript
	// text or an error if transcription fails.
	TranscribeAudio(ctx context.Context, audioData []byte, mimeType string) (string, error)
}

// OpenAITranscriber calls an OpenAI-compatible /v1/audio/transcriptions
// endpoint. The endpoint URL should be the base URL of the service (e.g.
// "http://localhost:8092"); the /v1/audio/transcriptions path is appended
// automatically.
type OpenAITranscriber struct {
	endpoint string
	model    string
	maxBytes int64
	client   *http.Client
}

// New creates an OpenAI-compatible transcription client.
func New(endpoint, model string, maxBytes int64, timeout time.Duration) *OpenAITranscriber {
	return &OpenAITranscriber{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		model:    model,
		maxBytes: maxBytes,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// TranscribeAudio uploads the audio data to the transcription endpoint and
// returns the transcript. The audio is sent as a multipart form upload with
// a "file" field (filename derived from the MIME type) and a "model" field.
func (t *OpenAITranscriber) TranscribeAudio(ctx context.Context, audioData []byte, mimeType string) (string, error) {
	if len(audioData) == 0 {
		return "", fmt.Errorf("transcribe: no audio data")
	}
	if len(audioData) > int(t.maxBytes) {
		return "", fmt.Errorf("transcribe: audio %d bytes exceeds max %d bytes", len(audioData), t.maxBytes)
	}

	// Build a filename from the MIME type so the server knows the format.
	ext := extFromMIME(mimeType)
	filename := "voice." + ext

	// Build multipart form: file=@audio, model=<name>
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("transcribe: creating form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("transcribe: writing audio data: %w", err)
	}
	if err := writer.WriteField("model", t.model); err != nil {
		return "", fmt.Errorf("transcribe: writing model field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("transcribe: closing multipart writer: %w", err)
	}

	url := t.endpoint + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", fmt.Errorf("transcribe: creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcribe: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap on response
	if err != nil {
		return "", fmt.Errorf("transcribe: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		snip := string(respBody)
		if len(snip) > 200 {
			snip = snip[:200]
		}
		return "", fmt.Errorf("transcribe: endpoint %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(snip))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("transcribe: parsing response: %w (raw: %s)", err, string(respBody))
	}

	transcript := strings.TrimSpace(result.Text)
	if transcript == "" {
		log.Printf("[transcribe] endpoint returned empty transcript for model %q", t.model)
	}
	return transcript, nil
}

// extFromMIME maps common audio MIME types to file extensions.
// Unknown types default to "ogg" (Telegram voice notes are OGG/Opus).
func extFromMIME(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/ogg", "application/ogg", "audio/opus":
		return "ogg"
	case "audio/webm", "audio/webm;codecs=opus":
		return "webm"
	case "audio/mp4", "audio/aac", "audio/x-aac":
		return "mp4"
	default:
		if mimeType != "" {
			// Use the subtype if present (e.g. "audio/ogg" -> "ogg")
			if i := strings.Index(mimeType, "/"); i >= 0 {
				sub := strings.SplitN(mimeType[i+1:], ";", 2)[0]
				sub = strings.TrimSpace(sub)
				if sub != "" && isSafeExt(sub) {
					return sub
				}
			}
		}
		return "ogg"
	}
}

// isSafeExt returns true if ext contains only safe filename characters.
func isSafeExt(ext string) bool {
	if ext == "" {
		return false
	}
	for _, r := range ext {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
