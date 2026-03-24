// Package telegram provides a Telegram Bot API gateway for FamClaw.
// Uses long-polling — no webhook required, works behind NAT/RPi.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/famclaw/famclaw/internal/gateway"
)

// Bot is a Telegram gateway using the Bot API with long-polling.
type Bot struct {
	token  string
	client *http.Client
}

// New creates a Telegram bot with the given token.
func New(token string) *Bot {
	return &Bot{
		token: token,
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

			msg := gateway.Message{
				Gateway:    "telegram",
				ExternalID: strconv.FormatInt(u.Message.From.ID, 10),
				Text:       u.Message.Text,
			}

			reply := handleMsg(ctx, msg)

			if err := b.sendMessage(ctx, u.Message.Chat.ID, reply.Text); err != nil {
				log.Printf("[telegram] send error: %v", err)
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
	ID int64 `json:"id"`
}

func (b *Bot) getUpdates(ctx context.Context, offset int) ([]tgUpdate, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", b.token, offset)
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
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("text", text)

	req, err := http.NewRequestWithContext(ctx, "POST", u+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("creating send request: %w", err)
	}

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
