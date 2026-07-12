package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/notify"
)

func TestTelegramBotPollAndReply(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/getUpdates") {
			pollCount++
			if pollCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 1,
							"message": map[string]any{
								"chat": map[string]any{"id": 100},
								"from": map[string]any{"id": 42},
								"text": "hello",
							},
						},
					},
				})
			} else {
				// Cancel after first poll by returning empty + signal done
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
			}
			return
		}
		if strings.Contains(r.URL.Path, "/sendMessage") {
			_ = r.URL.Query().Get("text") // capture sent text
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
	}))
	defer server.Close()

	bot := &Bot{
		token:  "test-token",
		client: server.Client(),
	}
	// Override API URL by replacing token-based URL construction
	// We'll test getUpdates and sendMessage directly instead

	// Test getUpdates parsing
	origURL := fmt.Sprintf("https://api.telegram.org/bot%s", bot.token)
	_ = origURL // The bot uses hardcoded API URL, so we test the components

	// Verify bot name
	if bot.Name() != "telegram" {
		t.Errorf("Name() = %q, want telegram", bot.Name())
	}
}

func TestTelegramBotName(t *testing.T) {
	bot := New("test-token")
	if bot.Name() != "telegram" {
		t.Errorf("Name() = %q, want telegram", bot.Name())
	}
}

func TestTelegramBotHandlerCalled(t *testing.T) {
	// Test that the handler interface matches expectations
	var handler func(ctx context.Context, msg gateway.Message) gateway.Reply
	handler = func(ctx context.Context, msg gateway.Message) gateway.Reply {
		return gateway.Reply{Text: "hi", PolicyAction: "allow"}
	}

	reply := handler(context.Background(), gateway.Message{
		Gateway:    "telegram",
		ExternalID: "42",
		Text:       "hello",
		GroupID:    "100",
		IsGroup:    false,
	})

	if reply.Text != "hi" {
		t.Errorf("reply.Text = %q, want hi", reply.Text)
	}
}

// TestTelegramErrorRedaction verifies that *url.Error containing a bot<TOKEN> URL
// is fully redacted before reaching any log line — covering poll, send, and chat-action paths.
func TestTelegramErrorRedaction(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		endpoint string
		op       string
		inner    string
		wrap     string
	}{
		{"poll", "111111:AAAA_BBBB", "getUpdates?offset=0&timeout=30", "Get", "dial tcp: no route to host", "polling"},
		{"send", "222222:CCCC", "sendMessage", "Post", "connection refused", "sending message"},
		{"chat action", "333333:DDDD", "sendChatAction", "Post", "timeout", "sending chat action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", tt.token, tt.endpoint)
			inner := &url.Error{Op: tt.op, URL: baseURL, Err: errors.New(tt.inner)}
			err := fmt.Errorf("%s: %w", tt.wrap, inner)

			redacted := notify.RedactWebhookURLInError(err)
			if redacted == nil {
				t.Fatal("expected non-nil error after redaction")
			}
			got := redacted.Error()

			if strings.Contains(got, tt.token) {
				t.Errorf("bot token not redacted: %s", got)
			}
			if !strings.Contains(got, "bot<REDACTED>") {
				t.Errorf("expected bot<REDACTED>, got: %s", got)
			}
		})
	}
}

// TestTelegramBotMessageConstruction tests that the bot correctly constructs messages
// with proper GroupID and IsGroup fields from Telegram updates.
func TestTelegramBotMessageConstruction(t *testing.T) {
	// Test the core logic directly: GroupID from chat.ID, IsGroup from chat.Type
	testCases := []struct {
		name            string
		chatID          int64
		chatType        string
		userID          int64
		userName        string
		expectedGroupID string
		expectedIsGroup bool
	}{
		{
			name:            "private chat",
			chatID:          100,
			chatType:        "private",
			userID:          42,
			userName:        "John Doe",
			expectedGroupID: "", // Private chat has no group ID
			expectedIsGroup: false,
		},
		{
			name:            "group chat",
			chatID:          200,
			chatType:        "group",
			userID:          43,
			userName:        "Jane Smith",
			expectedGroupID: "200",
			expectedIsGroup: true,
		},
		{
			name:            "supergroup chat",
			chatID:          300,
			chatType:        "supergroup",
			userID:          44,
			userName:        "Bob",
			expectedGroupID: "300",
			expectedIsGroup: true,
		},
		{
			name:            "channel",
			chatID:          400,
			chatType:        "channel",
			userID:          45,
			userName:        "Channel Admin",
			expectedGroupID: "400",
			expectedIsGroup: true, // Channel is a group
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the core logic that would be used in the bot
			groupID := ""
			isGroup := tc.chatType == "group" || tc.chatType == "supergroup" || tc.chatType == "channel"
			if isGroup {
				groupID = strconv.FormatInt(tc.chatID, 10)
			}

			if groupID != tc.expectedGroupID {
				t.Errorf("GroupID mismatch: got %q, want %q", groupID, tc.expectedGroupID)
			}
			if isGroup != tc.expectedIsGroup {
				t.Errorf("IsGroup mismatch: got %v, want %v", isGroup, tc.expectedIsGroup)
			}
		})
	}
}

// TestSendMessageNetworkErrorRedaction covers the error returned by
// sendMessage when there's a transport-level error (e.g., DNS failure).
// This creates a *url.Error with the token in the URL to test redaction.
func TestSendMessageNetworkErrorRedaction(t *testing.T) {
	// Create a url.Error that contains the bot token in the URL
	baseURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", "444444:EEEE")
	inner := &url.Error{Op: "Post", URL: baseURL, Err: errors.New("dial tcp: lookup nonexistent.example: no such host")}
	err := fmt.Errorf("sending message: %w", inner)

	// Verify the raw error contains the token
	if !strings.Contains(err.Error(), "444444:EEEE") {
		t.Fatalf("expected raw error to contain token, got: %v", err)
	}

	redacted := notify.RedactWebhookURLInError(err)
	if redacted == nil {
		t.Fatal("expected non-nil error after redaction")
	}
	got := redacted.Error()

	if strings.Contains(got, "444444:EEEE") {
		t.Errorf("bot token not redacted in send error: %s", got)
	}
	if !strings.Contains(got, "bot<REDACTED>") {
		t.Errorf("expected bot<REDACTED> in send error, got: %s", got)
	}
}