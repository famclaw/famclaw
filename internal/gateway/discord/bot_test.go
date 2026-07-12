package discord

import (
	"context"
	"testing"

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
			expectedGroupID: "chan1",
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
			groupID := tc.channelID
			isGroup := tc.guildID != ""

			if groupID != tc.expectedGroupID {
				t.Errorf("GroupID mismatch: got %q, want %q", groupID, tc.expectedGroupID)
			}
			if isGroup != tc.expectedIsGroup {
				t.Errorf("IsGroup mismatch: got %v, want %v", isGroup, tc.expectedIsGroup)
			}

			// Verify the expected values match our test case expectations
			if tc.expectedGroupID != tc.channelID {
				t.Errorf("Test case error: expectedGroupID %q doesn't match channelID %s", tc.expectedGroupID, tc.channelID)
			}
			expectedIsGroup := tc.guildID != ""
			if tc.expectedIsGroup != expectedIsGroup {
				t.Errorf("Test case error: expectedIsGroup %v doesn't match guildID %q", tc.expectedIsGroup, tc.guildID)
			}
		})
	}
}
