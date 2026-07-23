// Package discord provides a Discord gateway for FamClaw using discordgo.
package discord

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/notify"
)

// Bot is a Discord gateway.
type Bot struct {
	token string
	mu    sync.RWMutex
	session *discordgo.Session
}

// New creates a Discord bot with the given token.
func New(token string) *Bot {
	return &Bot{token: token}
}

func (b *Bot) Name() string { return "discord" }

// SendMessage delivers a bot-initiated (proactive) message to a user's
// Discord DM. externalID is the user's Discord snowflake ID. The gateway
// creates (or reuses) a DM channel with that user, then sends the text
// in chunks at Discord's 2000-character limit. Implements gateway.Sender
// so the reminder scheduler can fire reminders without an inbound message.
func (b *Bot) SendMessage(ctx context.Context, externalID, text string) error {
	b.mu.RLock()
	session := b.session
	b.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("discord session not started")
	}
	// Create (or fetch) a DM channel with the user.
	dm, err := session.UserChannelCreate(externalID)
	if err != nil {
		return fmt.Errorf("discord DM channel for %s: %w", externalID, err)
	}
	for _, chunk := range gateway.ChunkMessage(text, 2000) {
		if _, err := session.ChannelMessageSend(dm.ID, chunk); err != nil {
			return fmt.Errorf("discord send to %s: %w", externalID, err)
		}
	}
	return nil
}

// Start connects to Discord and listens for messages. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	session, err := discordgo.New("Bot " + b.token)
	if err != nil {
		return err
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	// Store the session so SendMessage (proactive delivery) can use it.
	b.mu.Lock()
	b.session = session
	b.mu.Unlock()

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
		msg := gateway.Message{
			Gateway:     "discord",
			ExternalID:  m.Author.ID,
			Text:        m.Content,
			DisplayName: displayName,
			GroupID:     groupID,
			IsGroup:     isGroup,
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

	// Clear the session reference on shutdown so SendMessage returns a
	// clear error rather than panicking on a closed session.
	defer func() {
		b.mu.Lock()
		b.session = nil
		b.mu.Unlock()
	}()

	log.Printf("[discord] connected as %s", session.State.User.Username)

	<-ctx.Done()
	return ctx.Err()
}
