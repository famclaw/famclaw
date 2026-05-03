//go:build integration

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/famclaw/famclaw/internal/gateway/discord"
)

func TestDiscord_SendMessage_Chunking(t *testing.T) {
	type captureEntry struct{ content string }
	var (
		mu       sync.Mutex
		recorded []captureEntry
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode error: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		mu.Lock()
		recorded = append(recorded, captureEntry{content: body.Content})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "100000000000000001",
			"channel_id": "100000000000000000",
			"content":    body.Content,
			"timestamp":  "2026-01-01T00:00:00Z",
			"author":     map[string]any{"id": "200000000000000000", "username": "test"},
		})
	}))
	defer ts.Close()

	origEndpoint := discordgo.EndpointChannelMessages
	discordgo.EndpointChannelMessages = func(channelID string) string {
		return ts.URL + "/api/v9/channels/" + channelID + "/messages"
	}
	t.Cleanup(func() { discordgo.EndpointChannelMessages = origEndpoint })

	sess, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}

	text := strings.Repeat("b", 3500)
	if err := discord.SendChunked(sess, "100000000000000000", text); err != nil {
		t.Fatalf("SendChunked: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(recorded) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(recorded))
	}
	for i, entry := range recorded {
		if len(entry.content) > 2000 {
			t.Errorf("chunk %d length = %d, exceeds 2000", i, len(entry.content))
		}
	}
	if joined := recorded[0].content + recorded[1].content; joined != text {
		t.Errorf("content mismatch: joined len=%d, want %d", len(joined), len(text))
	}
}
