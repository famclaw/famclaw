//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/gateway/telegram"
)

type capturedSend struct {
	text string
	when time.Time
}

func TestTelegram_SendMessage_Chunking(t *testing.T) {
	var (
		mu       sync.Mutex
		recorded []capturedSend
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected path: %s %s", r.Method, r.URL.Path)
			http.Error(w, "Not Found", 404)
			return
		}

		var body struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "Bad Request", 400)
			return
		}

		mu.Lock()
		recorded = append(recorded, capturedSend{text: body.Text, when: time.Now()})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 42,
				"date":       1700000000,
				"chat":       map[string]any{"id": 1001, "type": "private"},
				"text":       body.Text,
			},
		})
	}))
	defer ts.Close()

	bot := telegram.NewWithEndpoint("test-token", ts.URL)

	text := strings.Repeat("a", 5000)
	ctx := context.Background()
	if err := bot.SendChunked(ctx, 1001, text); err != nil {
		t.Fatalf("SendChunked: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(recorded) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(recorded))
	}
	for i, entry := range recorded {
		if len(entry.text) > 4096 {
			t.Errorf("chunk %d length = %d, exceeds 4096", i, len(entry.text))
		}
	}
	if joined := recorded[0].text + recorded[1].text; joined != text {
		t.Errorf("chunks did not concatenate to original input: joined len=%d, want %d", len(joined), len(text))
	}
}
