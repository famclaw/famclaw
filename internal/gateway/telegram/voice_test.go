package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
)

// TestTelegramVoiceNoteAttachment is an integration test that verifies a
// Telegram voice note is downloaded and delivered to the handler as an
// audio gateway.Attachment with the correct MIME type and base64-encoded
// OGG/Opus data.
func TestTelegramVoiceNoteAttachment(t *testing.T) {
	voiceFileData := []byte("fake-ogg-opus-data")
	updateCallCount := 0
	getFileCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/getUpdates"):
			updateCallCount++
			if updateCallCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 1,
							"message": map[string]any{
								"chat":  map[string]any{"id": 100, "type": "private"},
								"from":  map[string]any{"id": 42, "first_name": "John"},
								"voice": map[string]any{
									"file_id":   "voice123",
									"file_size": 100,
									"mime_type": "audio/ogg",
								},
							},
						},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
			}
		case strings.Contains(path, "/getFile"):
			getFileCallCount++
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":   "voice123",
					"file_path": "voice/file123",
				},
			})
		case strings.Contains(path, "/file/bot"):
			w.Write(voiceFileData)
		case strings.Contains(path, "/sendChatAction"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		case strings.Contains(path, "/sendMessage"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	bot := NewWithEndpoint("test-token", server.URL)

	var captured gateway.Message
	var handlerCalled bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(_ context.Context, msg gateway.Message) gateway.Reply {
		captured = msg
		handlerCalled = true
		cancel() // stop polling after first message
		return gateway.Reply{Text: "ok", PolicyAction: "allow"}
	}

	done := make(chan struct{})
	go func() {
		_ = bot.Start(ctx, handler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for bot to process voice note")
	}

	if !handlerCalled {
		t.Fatal("handler was never called")
	}
	if captured.Gateway != "telegram" {
		t.Errorf("gateway = %q, want telegram", captured.Gateway)
	}
	if captured.ExternalID != "42" {
		t.Errorf("external_id = %q, want 42", captured.ExternalID)
	}
	if len(captured.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(captured.Attachments))
	}

	att := captured.Attachments[0]
	if att.Type != "audio" {
		t.Errorf("attachment type = %q, want audio", att.Type)
	}
	if att.MIMEType != "audio/ogg" {
		t.Errorf("mime type = %q, want audio/ogg", att.MIMEType)
	}
	decoded, err := base64.StdEncoding.DecodeString(att.Data)
	if err != nil {
		t.Fatalf("decoding audio base64: %v", err)
	}
	if string(decoded) != string(voiceFileData) {
		t.Errorf("audio data = %q, want %q", string(decoded), string(voiceFileData))
	}
	if getFileCallCount != 1 {
		t.Errorf("getFile called %d times, want 1", getFileCallCount)
	}
}

// TestTelegramAudioAttachment verifies that a general audio file
// (not a voice note) is also downloaded and delivered as an audio
// attachment, using the audio's declared MIME type.
func TestTelegramAudioAttachment(t *testing.T) {
	audioFileData := []byte("fake-mp3-data")
	updateCallCount := 0
	getFileCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/getUpdates"):
			updateCallCount++
			if updateCallCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 1,
							"message": map[string]any{
								"chat": map[string]any{"id": 100, "type": "private"},
								"from": map[string]any{"id": 42, "first_name": "John"},
								"audio": map[string]any{
									"file_id":   "audio123",
									"file_size": 100,
									"mime_type": "audio/mpeg",
								},
							},
						},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
			}
		case strings.Contains(path, "/getFile"):
			getFileCallCount++
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":   "audio123",
					"file_path": "audio/file123",
				},
			})
		case strings.Contains(path, "/file/bot"):
			w.Write(audioFileData)
		case strings.Contains(path, "/sendChatAction"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		case strings.Contains(path, "/sendMessage"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	bot := NewWithEndpoint("test-token", server.URL)

	var captured gateway.Message
	var handlerCalled bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(_ context.Context, msg gateway.Message) gateway.Reply {
		captured = msg
		handlerCalled = true
		cancel()
		return gateway.Reply{Text: "ok", PolicyAction: "allow"}
	}

	done := make(chan struct{})
	go func() {
		_ = bot.Start(ctx, handler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for bot to process audio file")
	}

	if !handlerCalled {
		t.Fatal("handler was never called")
	}
	if len(captured.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(captured.Attachments))
	}
	att := captured.Attachments[0]
	if att.Type != "audio" {
		t.Errorf("attachment type = %q, want audio", att.Type)
	}
	if att.MIMEType != "audio/mpeg" {
		t.Errorf("mime type = %q, want audio/mpeg", att.MIMEType)
	}
	decoded, err := base64.StdEncoding.DecodeString(att.Data)
	if err != nil {
		t.Fatalf("decoding audio base64: %v", err)
	}
	if string(decoded) != string(audioFileData) {
		t.Errorf("audio data mismatch")
	}
	if getFileCallCount != 1 {
		t.Errorf("getFile called %d times, want 1", getFileCallCount)
	}
}

// TestTelegramVoiceAndPhotoCoexist verifies that a message carrying both
// a photo and a voice note produces two attachments (image + audio).
func TestTelegramVoiceAndPhotoCoexist(t *testing.T) {
	updateCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/getUpdates"):
			updateCallCount++
			if updateCallCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 1,
							"message": map[string]any{
								"chat": map[string]any{"id": 100, "type": "private"},
								"from": map[string]any{"id": 42, "first_name": "John"},
								"photo": []map[string]any{
									{"file_id": "photo123", "file_size": 100, "width": 100, "height": 100},
								},
								"voice": map[string]any{
									"file_id":   "voice123",
									"file_size": 100,
									"mime_type": "audio/ogg",
								},
							},
						},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
			}
		case strings.Contains(path, "/getFile"):
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":   "req",
					"file_path": "file/data",
				},
			})
		case strings.Contains(path, "/file/bot"):
			w.Write([]byte("data"))
		case strings.Contains(path, "/sendChatAction"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		case strings.Contains(path, "/sendMessage"):
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	bot := NewWithEndpoint("test-token", server.URL)

	var captured gateway.Message
	var handlerCalled bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(_ context.Context, msg gateway.Message) gateway.Reply {
		captured = msg
		handlerCalled = true
		cancel()
		return gateway.Reply{Text: "ok", PolicyAction: "allow"}
	}

	done := make(chan struct{})
	go func() {
		_ = bot.Start(ctx, handler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}

	if !handlerCalled {
		t.Fatal("handler was never called")
	}
	if len(captured.Attachments) != 2 {
		t.Fatalf("expected 2 attachments (image+audio), got %d", len(captured.Attachments))
	}
	// Find the audio one
	var hasAudio, hasImage bool
	for _, att := range captured.Attachments {
		if att.Type == "audio" {
			hasAudio = true
		}
		if att.Type == "image" {
			hasImage = true
		}
	}
	if !hasAudio {
		t.Error("expected an audio attachment")
	}
	if !hasImage {
		t.Error("expected an image attachment")
	}
}
