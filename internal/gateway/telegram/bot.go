// Package telegram provides a Telegram Bot API gateway for FamClaw.
// Uses long-polling — no webhook required, works behind NAT/RPi.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
)

// Bot is a Telegram gateway using the Bot API with long-polling.
type Bot struct {
	token    string
	endpoint string // Bot API base URL (defaults to https://api.telegram.org)
	client   *http.Client
}

// defaultEndpoint is the public Telegram Bot API host.
const defaultEndpoint = "https://api.telegram.org"

// New creates a Telegram bot with the given token.
func New(token string) *Bot {
	return &Bot{
		token:    token,
		endpoint: defaultEndpoint,
		client: &http.Client{
			Timeout: 60 * time.Second, // long-poll timeout
		},
	}
}

func (b *Bot) Name() string { return "telegram" }

// Start begins long-polling for updates. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			log.Printf("[telegram] poll error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Text == "" {
				continue
			}

			displayName := strings.TrimSpace(u.Message.From.FirstName + " " + u.Message.From.LastName)
			if displayName == "" {
				displayName = u.Message.From.Username
			}

			msg := gateway.Message{
				Gateway:     "telegram",
				ExternalID:  strconv.FormatInt(u.Message.From.ID, 10),
				Text:        u.Message.Text,
				DisplayName: displayName,
			}

			// Typing indicator. Telegram's chat action expires after ~5s,
			// so we refresh every 4s for the duration of agent processing.
			// Per UX commitment §11: never silent failure — show the user
			// Butler is working rather than letting a 20-30s LLM call look
			// like a hung bot.
			stopTyping := make(chan struct{})
			go func(chatID int64) {
				_ = b.sendChatAction(ctx, chatID, "typing")
				t := time.NewTicker(4 * time.Second)
				defer t.Stop()
				for {
				select {
				case <-ctx.Done():
				return
				case <-stopTyping:
				return
				case <-t.C:
				        _ = b.sendChatAction(ctx, chatID, "typing")
                            }
                        }
			}(u.Message.Chat.ID)

			reply := handleMsg(ctx, msg)
			close(stopTyping)

			// Skip whitespace-only replies — both platforms reject empty
			// messages with a 4xx, leaving the user with no visible feedback.
			// The agent layer now substitutes a fallback for empty LLM
			// output (see internal/agent/agent.go) so this should be rare,
			// but keep the guard as defense in depth.
			if strings.TrimSpace(reply.Text) == "" {
				continue
			}

			// Normalize for chat-gateway rendering: strip <br> tags, convert
			// markdown tables to bullet lists, collapse excess blank lines.
			// Code blocks (triple-backtick fences) are preserved verbatim.
			text := gateway.NormalizeReplyForChatGateway(reply.Text)

			// Convert remaining markdown bold/italic/code to HTML so
			// Telegram's parse_mode="HTML" renders them. Plain text falls
			// through unchanged.
			text = markdownToTelegramHTML(text)

			// Chunk at Telegram's 4096-byte message limit. Break on first
			// error so we don't spam if the channel is gone or rate-limited.
			for _, chunk := range gateway.ChunkMessage(text, 4096) {
				if err := b.sendMessage(ctx, u.Message.Chat.ID, chunk); err != nil {
					log.Printf("[telegram] send error: %v", err)
					break
				}
			}
		}
	}
}

type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Chat tgChat `json:"chat"`
	From tgUser `json:"from"`
	Text string `json:"text"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

func (b *Bot) getUpdates(ctx context.Context, offset int) ([]tgUpdate, error) {
	u := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=30", b.endpoint, b.token, offset)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("polling: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s", string(body))
	}
	return result.Result, nil
}

func (b *Bot) sendMessage(ctx context.Context, chatID int64, text string) error {
	u := fmt.Sprintf("%s/bot%s/sendMessage", b.endpoint, b.token)
	jsonBody, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	if err != nil {
		return fmt.Errorf("marshaling send body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating send request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram send error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// sendChatAction posts a sendChatAction call to show "Butler is typing..."
// in the Telegram client. Best-effort — errors are not surfaced (the
// caller wants the typing UX, not a hard dependency on it).
func (b *Bot) sendChatAction(ctx context.Context, chatID int64, action string) error {
	u := fmt.Sprintf("%s/bot%s/sendChatAction", b.endpoint, b.token)
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
