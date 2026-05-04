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
	const discordLimit = 2000

	tests := []struct {
		name      string
		inputLen  int
		wantParts int
	}{
		{"exactly_2000", discordLimit, 1},
		{"just_over_2001", discordLimit + 1, 2},
		{"large_3500", 3500, 2},
		{"two_full_4000", discordLimit * 2, 2},
		{"three_chunks_5000", 5000, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			text := strings.Repeat("b", tt.inputLen)
			if err := discord.SendChunked(sess, "100000000000000000", text); err != nil {
				t.Fatalf("SendChunked: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			if len(recorded) != tt.wantParts {
				t.Fatalf("expected %d chunks, got %d", tt.wantParts, len(recorded))
			}
			var joined strings.Builder
			for i, entry := range recorded {
				if len(entry.content) > discordLimit {
					t.Errorf("chunk %d length = %d, exceeds %d", i, len(entry.content), discordLimit)
				}
				joined.WriteString(entry.content)
			}
			if joined.String() != text {
				t.Errorf("content mismatch: joined len=%d, want %d", joined.Len(), len(text))
			}
		})
	}
}
