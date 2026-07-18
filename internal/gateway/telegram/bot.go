// Package telegram provides a Telegram Bot API gateway for FamClaw.
// Uses long-polling — no webhook required, works behind NAT/RPi.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"github.com/famclaw/famclaw/internal/imageutil"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/imageutil"
)
const maxImageBytes = 5 * 1024 * 1024 // 5MB cap (RPi-friendly)
const maxImageDimension = 1280 // Maximum dimension in pixels (preserve aspect ratio)
// Bot is a Telegram gateway using the Bot API with long-polling.
const maxImageDimension = 1280 // Maximum dimension in pixels (preserve aspect ratio)
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
			log.Printf("[telegram] poll error: %v", notify.RedactWebhookURLInError(err))
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
			// Check if this is a photo message
			var photoData []byte
			var mimeType string
			if u.Message.Photo != nil && len(u.Message.Photo) > 0 {
				// Get the largest photo (last one in the array)
				photoSize := u.Message.Photo[len(u.Message.Photo)-1]
				
				// Download the photo file
				fileInfo, err := b.getFile(ctx, photoSize.FileID)
				if err != nil {
					log.Printf("[telegram] failed to get file info: %v", err)
				} else if fileInfo != nil {
					// Download the actual file data
					origData, err := b.downloadFile(ctx, fileInfo.FilePath)
					if err != nil {
						log.Printf("[telegram] failed to download file: %v", err)
					} else {
						// Resize image if needed
						resizedData, err := imageutil.ResizeImage(origData, maxImageDimension)
						if err != nil {
							log.Printf("[telegram] failed to resize image: %v", err)
							photoData = nil
						} else {
							// Check if we actually resized
							if len(resizedData) != len(origData) || !bytes.Equal(resizedData, origData) {
								// We resized, so set mimeType to JPEG
								mimeType = "image/jpeg"
							} else {
								// We did not resize, so determine MIME type from file extension
								if strings.HasSuffix(strings.ToLower(fileInfo.FilePath), ".png") {
									mimeType = "image/png"
								} else {
									mimeType = "image/jpeg"
								}
							}
							photoData = resizedData
						}
					}
					// Check the size after potential resize
					if len(photoData) > maxImageBytes {
						log.Printf("[telegram] image %d bytes exceeds %d cap, skipping", len(photoData), maxImageBytes)
						photoData = nil
					}
				}
			}
			// Prepare attachments if we have photo data
			var attachments []gateway.Attachment
			if len(photoData) > 0 {
				attachments = []gateway.Attachment{{
					Type:     "image",
					Data:     base64.StdEncoding.EncodeToString(photoData),
					MIMEType: mimeType,
				}}
			}
			
			// Group info (moved after photo processing to maintain variable scope)
			isGroup := u.Message.Chat.Type == "group" || u.Message.Chat.Type == "supergroup" || u.Message.Chat.Type == "channel"
			groupID := ""
			if isGroup {
				groupID = strconv.FormatInt(u.Message.Chat.ID, 10)
			}
			msg := gateway.Message{
				Gateway:     "telegram",
				ExternalID:  strconv.FormatInt(u.Message.From.ID, 10),
				Text:        u.Message.Text,
				DisplayName: displayName,
				GroupID:     groupID,
				IsGroup:     isGroup,
				Attachments: attachments,
			}

			// Typing indicator. Telegram's chat action expires after ~5s,
			// so we refresh every 4s for the duration of agent processing.
			// Per UX commitment §11: never silent failure — show the user
			// Butler is working rather than letting a 20-30s LLM call look
			// like a hung bot.
			stopTyping := make(chan struct{})
			go func(chatID int64) {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := b.sendChatAction(ctx, chatID, "typing"); err != nil {
					log.Printf("[telegram] typing indicator: %v", notify.RedactWebhookURLInError(err))
				}
				t := time.NewTicker(4 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-stopTyping:
						return
					case <-t.C:
						if err := b.sendChatAction(ctx, chatID, "typing"); err != nil {
							log.Printf("[telegram] typing indicator: %v", notify.RedactWebhookURLInError(err))
						}
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

			// Chunk BEFORE HTML conversion. Chunking on rendered HTML can
			// split an opening tag from its closing tag across chunks (e.g.
			// "<b>... " in chunk N and "...</b>" in chunk N+1) — Telegram's
			// strict HTML parser rejects malformed messages with 400. By
			// chunking on the markdown source first and converting each
			// chunk independently, tag pairs stay within a single chunk.
			//
			// Chunk budget is a touch under the 4096-byte API limit so the
			// expansion of "**" → "<b></b>" (-1 byte) and "`" → "<code>...</code>"
			// (+10 bytes) doesn't push a chunk over.
			const chunkBudget = 3800
			for _, raw := range gateway.ChunkMessage(text, chunkBudget) {
				chunk := markdownToTelegramHTML(raw)
				if err := b.sendMessage(ctx, u.Message.Chat.ID, chunk); err != nil {
					log.Printf("[telegram] send error: %v", notify.RedactWebhookURLInError(err))
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
	Photo []tgPhoto `json:"photo,omitempty"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private, group, supergroup, channel
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}
// tgPhoto represents a photo size.
type tgPhoto struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
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

// Send implements gateway.Sender for outbound messages (e.g., reminders).
func (b *Bot) Send(ctx context.Context, chatID string, text string) error {
	// chatID for Telegram can be a user ID or group chat ID (as string)
	// Convert to int64 for the API
	var id int64
	if _, err := fmt.Sscanf(chatID, "%d", &id); err != nil {
		return fmt.Errorf("invalid telegram chat_id %q: %w", chatID, err)
	}

	// Chunk and normalize the message like the inbound handler does
	const chunkBudget = 3800
	for _, raw := range gateway.ChunkMessage(text, chunkBudget) {
		chunk := markdownToTelegramHTML(raw)
		if err := b.sendMessage(ctx, id, chunk); err != nil {
			return err
		}
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
		return fmt.Errorf("marshaling chat action: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating chat action request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending chat action: %w", err)
	}
	resp.Body.Close()
	return nil
}
// getFile retrieves file information from the Telegram Bot API.
func (b *Bot) getFile(ctx context.Context, fileID string) (*tgFile, error) {
	u := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", b.endpoint, b.token, fileID)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating getFile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getFile request failed: %w", err)
	}
	defer resp.Body.Close()

	var body []byte
	
	if resp.StatusCode != http.StatusOK {
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading getFile response body: %w", err)
		}
		return nil, fmt.Errorf("telegram getFile error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		OK     bool     `json:"ok"`
		Result *tgFile  `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing getFile response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s", string(body))
	}

	return result.Result, nil
}

// downloadFile downloads a file from the Telegram Bot API.
func (b *Bot) downloadFile(ctx context.Context, filePath string) ([]byte, error) {
	u := fmt.Sprintf("%s/file/bot%s/%s", b.endpoint, b.token, filePath)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating downloadFile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloadFile request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telegram downloadFile error %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// tgFile represents a file ready for download from Telegram.
type tgFile struct {
	FileID     string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize   int    `json:"file_size,omitempty"`
	FilePath   string `json:"file_path"`
}
