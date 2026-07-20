// Package discord provides a Discord gateway for FamClaw using discordgo.
package discord

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/imageutil"
	"github.com/famclaw/famclaw/internal/notify"
)

// mimeToExtensions maps MIME types to allowed file extensions.
var mimeToExtensions = map[string][]string{
	"image/jpeg":      {`.jpg`, `.jpeg`},
	"image/jpg":       {`.jpg`, `.jpeg`},
	"image/png":       {`.png`},
	"image/gif":       {`.gif`},
	"image/webp":      {`.webp`},
	"text/plain":      {`.txt`},
	"application/pdf": {`.pdf`},
	"application/zip": {`.zip`},
	// Add more as needed
}

// validateMIMEExtension checks that the file extension matches the MIME type.
func validateMIMEExtension(mimeType string, fileName string) error {
	exts, ok := mimeToExtensions[mimeType]
	if !ok {
		// If we don't know the MIME type, we cannot validate.
		// For security, we treat unknown MIME types as invalid.
		return fmt.Errorf("unsupported MIME type: %s", mimeType)
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	for _, allowed := range exts {
		if ext == allowed {
			return nil
		}
	}
	return fmt.Errorf("file extension %q does not match MIME type %s (allowed extensions: %v)", ext, mimeType, exts)
}

// Bot is a Discord gateway.
type Bot struct {
	token       string
	session     *discordgo.Session
	sandboxRoot string
}

// New creates a Discord bot with the given token.
func New(token string) *Bot {
	return &Bot{token: token}
}

// NewWithSandbox creates a Discord bot with the given token and sandbox root.
func NewWithSandbox(token string, sandboxRoot string) *Bot {
	return &Bot{token: token, sandboxRoot: sandboxRoot}
}

func (b *Bot) Name() string { return "discord" }

// Start connects to Discord and listens for messages. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	session, err := discordgo.New("Bot " + b.token)
	if err != nil {
		return err
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	b.session = session

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Ignore own messages
		if m.Author.ID == s.State.User.ID {
			return
		}

		displayName := m.Author.GlobalName
		if displayName == "" {
			displayName = m.Author.Username
		}

		isGroup := m.GuildID != ""
		groupID := ""
		if isGroup {
			groupID = m.ChannelID
		}

		// Process attachments
		var attachments []gateway.Attachment
		var fileAttachmentNotes []string
		if len(m.Message.Attachments) > 0 {
			attachments = make([]gateway.Attachment, 0)
			for _, attachment := range m.Message.Attachments {
				// Check if it's an image attachment
				if strings.HasPrefix(attachment.ContentType, "image/") {
					// Validate size (5MB limit)
					if attachment.Size > imageutil.MaxImageBytes {
						log.Printf("[discord] image %d bytes exceeds %d cap, skipping", attachment.Size, imageutil.MaxImageBytes)
						continue
					}

					// Download image data
					imageData, err := downloadImage(ctx, attachment.URL)
					if err != nil {
						log.Printf("[discord] failed to download image: %v", err)
						continue
					}

					// Base64 encode and add to attachments
					attachments = append(attachments, gateway.Attachment{
						Type:     "image",
						Data:     base64.StdEncoding.EncodeToString(imageData),
						MIMEType: attachment.ContentType,
					})
				} else {
					// Handle non-image attachments (files)
					// Only process files under 100MB (reasonable limit)
					const maxFileSize = 25 * 1024 * 1024 // 25MB
					if attachment.Size > maxFileSize {
						log.Printf("[discord] file %d bytes exceeds %d cap, skipping", attachment.Size, maxFileSize)
						continue
					}

					// Download file data
					fileData, err := downloadFile(ctx, attachment.URL, maxFileSize)
					if err != nil {
						log.Printf("[discord] failed to download file: %v", err)
						continue
					}

					// Validate MIME type and extension consistency
					if err := validateMIMEExtension(attachment.ContentType, attachment.Filename); err != nil {
						log.Printf("[discord] attachment MIME-extension mismatch: %v", err)
						continue
					}

					// Write file to sandbox if sandbox is configured
					if b.sandboxRoot != "" {
						// Write to sandbox
						relPath, err := writeAttachmentToFile(ctx, b.sandboxRoot, attachment.Filename, fileData)
						if err != nil {
							log.Printf("[discord] failed to write file to sandbox: %v", err)
							continue
						}

						// Add note about saved file
						fileAttachmentNotes = append(fileAttachmentNotes, fmt.Sprintf("Saved attachment: %s", relPath))
					}
				}
			}
		}

		// Construct the message text with file attachment notes
		var parts []string
		var messageText string
		if m.Content != "" {
			parts = append(parts, m.Content)
		}
		if len(fileAttachmentNotes) > 0 {
			parts = append(parts, strings.Join(fileAttachmentNotes, "\n"))
		}
		messageText = strings.Join(parts, "\n\n")

		msg := gateway.Message{
			Gateway:     "discord",
			ExternalID:  m.Author.ID,
			Text:        messageText,
			DisplayName: displayName,
			GroupID:     groupID,
			IsGroup:     isGroup,
			Attachments: attachments,
		}

		// Typing indicator. Discord's typing state expires after ~10s, so
		// we refresh every 8s for the duration of agent processing. Lets
		// the user see "Butler is typing..." while the LLM thinks, instead
		// of a silent 20-30s wait that looks identical to a hung bot.
		// (UX commitment §11: never silent failure.)
		stopTyping := make(chan struct{})
		go func() {
			// Fire once immediately so the indicator shows up before the
			// first 8s tick.
			_ = s.ChannelTyping(m.ChannelID)
			t := time.NewTicker(8 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopTyping:
					return
				case <-t.C:
					_ = s.ChannelTyping(m.ChannelID)
				}
			}
		}()

		reply := handleMsg(ctx, msg)
		close(stopTyping)

		// Skip whitespace-only replies — Discord rejects empty messages.
		// The agent layer now substitutes a fallback for empty LLM output
		// (see internal/agent/agent.go) so this should be rare, but keep
		// the guard as defense in depth.
		if strings.TrimSpace(reply.Text) == "" {
			return
		}

		// Normalize for chat-gateway rendering: strip <br> tags, convert
		// markdown tables to bullet lists, collapse excess blank lines.
		// Code blocks (triple-backtick fences) are preserved verbatim.
		text := gateway.NormalizeReplyForChatGateway(reply.Text)

		// Chunk at Discord's 2000-character message limit. Break on first
		// error so we don't spam if the channel is gone or rate-limited.
		for _, chunk := range gateway.ChunkMessage(text, 2000) {
			if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
				log.Printf("[discord] send error: %v", notify.RedactWebhookURLInError(err))
				break
			}
		}
	})

	if err := session.Open(); err != nil {
		return err
	}
	defer session.Close()

	log.Printf("[discord] connected as %s", session.State.User.Username)

	<-ctx.Done()
	return ctx.Err()
}

// Send implements gateway.Sender for outbound messages (e.g., reminders).
func (b *Bot) Send(ctx context.Context, channelID string, text string) error {
	if b.session == nil {
		return fmt.Errorf("discord session not initialized")
	}
	// Chunk at Discord's 2000-character message limit
	for _, chunk := range gateway.ChunkMessage(text, 2000) {
		if _, err := b.session.ChannelMessageSend(channelID, chunk); err != nil {
			return fmt.Errorf("sending discord message: %w", err)
		}
	}
	return nil
}

// downloadImage downloads image data from a URL.
func downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Set a User-Agent header to avoid being blocked by some servers
	req.Header.Set("User-Agent", "FamClaw/1.0")

	// Use a client with timeout to avoid hanging
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download failed with status: %d", resp.StatusCode)
	}

	// Validate Content-Type is an image
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("downloaded file is not an image: %s", contentType)
	}

	// Read the image data
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading image data: %w", err)
	}

	// Validate image size (5MB limit)
	if len(data) > imageutil.MaxImageBytes {
		return nil, fmt.Errorf("image size %d exceeds %d byte limit", len(data), imageutil.MaxImageBytes)
	}

	return data, nil
}

// downloadFile downloads file data from a URL.
func downloadFile(ctx context.Context, url string, maxSize int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Set a User-Agent header to avoid being blocked by some servers
	req.Header.Set("User-Agent", "FamClaw/1.0")

	// Use a client with timeout to avoid hanging
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("file download failed with status: %d", resp.StatusCode)
	}

	// Read the file data
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading file data: %w", err)
	}

	// Validate file size
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file size %d exceeds %d byte limit", len(data), maxSize)
	}

	return data, nil
}

// writeAttachmentToFile writes attachment data to a file in the sandbox root.
// Returns the relative path to the file in the sandbox.
func writeAttachmentToFile(ctx context.Context, sandboxRoot string, fileName string, data []byte) (string, error) {
	// Reduce filename to its base component to prevent path traversal
	name := filepath.Base(fileName)
	// Reject empty/dot names
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid attachment filename: %q", fileName)
	}
	// Build target path inside sandbox
	target := filepath.Join(sandboxRoot, name)
	target = filepath.Clean(target)
	// Verify target is inside sandboxRoot (no path traversal)
	absSandbox, err := filepath.Abs(sandboxRoot)
	if err != nil {
		return "", fmt.Errorf("getting absolute sandbox path: %w", err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("getting absolute target path: %w", err)
	}
	rel, err := filepath.Rel(absSandbox, absTarget)
	if err != nil {
		return "", fmt.Errorf("getting relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("attachment filename attempts to escape sandbox: %q", fileName)
	}
	// Ensure the sandbox directory (parent of file) exists with secure permissions before writing
	dir := filepath.Dir(absTarget)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating directory for attachment: %w", err)
	}
	// Write file to sandbox
	if err := os.WriteFile(absTarget, data, 0o600); err != nil {
		return "", fmt.Errorf("writing attachment to sandbox: %w", err)
	}
	// Return relative path within sandbox
	relPath, err := filepath.Rel(sandboxRoot, absTarget)
	if err != nil {
		return "", fmt.Errorf("calculating relative path: %w", err)
	}
	return relPath, nil
}
