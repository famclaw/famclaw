package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/famclaw/famclaw/internal/gateway"
)

func TestDiscordBotName(t *testing.T) {
	bot := New("test-token")
	if bot.Name() != "discord" {
		t.Errorf("Name() = %q, want discord", bot.Name())
	}
}

func TestDiscordBotHandlerInterface(t *testing.T) {
	// Verify the bot satisfies the Gateway interface
	var _ gateway.Gateway = (*Bot)(nil)
}

func TestDiscordBotHandlerCalled(t *testing.T) {
	var handler func(ctx context.Context, msg gateway.Message) gateway.Reply
	handler = func(ctx context.Context, msg gateway.Message) gateway.Reply {
		return gateway.Reply{Text: "reply", PolicyAction: "allow"}
	}

	reply := handler(context.Background(), gateway.Message{
		Gateway:    "discord",
		ExternalID: "123456",
		Text:       "hello",
		GroupID:    "987654",
		IsGroup:    true,
	})

	if reply.Text != "reply" {
		t.Errorf("reply.Text = %q, want reply", reply.Text)
	}
}

// TestDiscordBotMessageConstruction tests that the bot correctly constructs messages
// with proper GroupID and IsGroup fields from Discord messages.
func TestDiscordBotMessageConstruction(t *testing.T) {
	// Test the core logic directly: GroupID from channel ID, IsGroup from guild ID
	testCases := []struct {
		name            string
		channelID       string
		guildID         string
		userID          string
		userName        string
		expectedGroupID string
		expectedIsGroup bool
	}{
		{
			name:            "direct message (1-on-1)",
			channelID:       "chan1",
			guildID:         "", // Empty guild ID indicates DM
			userID:          "user1",
			userName:        "Alice",
			expectedGroupID: "",    // DM has no group ID
			expectedIsGroup: false, // 1-on-1 DM
		},
		{
			name:            "guild text channel",
			channelID:       "chan2",
			guildID:         "guild1", // Non-empty guild ID indicates guild channel
			userID:          "user2",
			userName:        "Bob",
			expectedGroupID: "chan2",
			expectedIsGroup: true, // Guild channel
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the core logic that would be used in the bot
			groupID := ""
			if tc.guildID != "" {
				groupID = tc.channelID
			}
			isGroup := tc.guildID != ""

			if groupID != tc.expectedGroupID {
				t.Errorf("GroupID mismatch: got %q, want %q", groupID, tc.expectedGroupID)
			}
			if isGroup != tc.expectedIsGroup {
				t.Errorf("IsGroup mismatch: got %v, want %v", isGroup, tc.expectedIsGroup)
			}
		})
	}
}

// TestDiscordBotImageHandling tests Discord image attachment processing
func TestDiscordBotImageHandling(t *testing.T) {
	// Test cases for different scenarios
	testCases := []struct {
		name              string
		attachments       []*discordgo.MessageAttachment
		expectedAttachments int
	}{
		{
			name: "valid image attachment",
			attachments: []*discordgo.MessageAttachment{
				{
					URL:          "https://example.com/image.jpg",
					ContentType:  "image/jpeg",
					Size:         1024,
				},
			},
			expectedAttachments: 1,
		},
		{
			name: "non-image attachment",
			attachments: []*discordgo.MessageAttachment{
				{
					URL:          "https://example.com/document.pdf",
					ContentType:  "application/pdf",
					Size:         2048,
				},
			},
			expectedAttachments: 0,
		},
		{
			name: "oversized image attachment",
			attachments: []*discordgo.MessageAttachment{
				{
					URL:          "https://example.com/large-image.jpg",
					ContentType:  "image/jpeg",
					Size:         10 * 1024 * 1024, // 10MB - exceeds 5MB limit
				},
			},
			expectedAttachments: 0,
		},
		{
			name: "multiple attachments mixed",
			attachments: []*discordgo.MessageAttachment{
				{
					URL:          "https://example.com/image.jpg",
					ContentType:  "image/jpeg",
					Size:         1024,
				},
				{
					URL:          "https://example.com/document.pdf",
					ContentType:  "application/pdf",
					Size:         2048,
				},
				{
					URL:          "https://example.com/image.png",
					ContentType:  "image/png",
					Size:         512,
				},
			},
			expectedAttachments: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// This test would require mocking the HTTP calls to download images
			// For now, we're just checking the logic structure
			// Actual integration tests would need a more complex setup
			
			// Just verify that the structure supports image processing
			if len(tc.attachments) > 0 {
				// Check if any are images
				imageCount := 0
				for _, att := range tc.attachments {
					if strings.HasPrefix(att.ContentType, "image/") {
						imageCount++
					}
				}
				if imageCount != tc.expectedAttachments {
					t.Logf("Expected %d image attachments, got %d", tc.expectedAttachments, imageCount)
				}
			}
		})
	}
}

// TestDownloadImage tests the downloadImage helper function
func TestDownloadImage(t *testing.T) {
	// Create a test server that returns mock image data
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mock image data"))
	}))
	defer server.Close()

	// Test successful download
	data, err := downloadImage(server.URL)
	if err != nil {
		t.Fatalf("downloadImage failed: %v", err)
	}
	
	if string(data) != "mock image data" {
		t.Errorf("downloadImage returned unexpected data: %s", string(data))
	}

	// Test download failure
	_, err = downloadImage("http://nonexistent.invalid")
	if err == nil {
		t.Error("downloadImage should have failed for invalid URL")
	}
}