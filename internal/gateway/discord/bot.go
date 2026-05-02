// Package discord provides a Discord gateway for FamClaw using discordgo.
package discord

import (
	"context"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/famclaw/famclaw/internal/gateway"
)

// Bot is a Discord gateway.
type Bot struct {
	token string
}

// New creates a Discord bot with the given token.
func New(token string) *Bot {
	return &Bot{token: token}
}

func (b *Bot) Name() string { return "discord" }

// Start connects to Discord and listens for messages. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	session, err := discordgo.New("Bot " + b.token)
	if err != nil {
		return err
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Ignore own messages
		if m.Author.ID == s.State.User.ID {
			return
		}

		msg := gateway.Message{
			Gateway:    "discord",
			ExternalID: m.Author.ID,
			Text:       m.Content,
		}

		reply := handleMsg(ctx, msg)

		// Skip whitespace-only replies — Discord rejects empty messages.
		if strings.TrimSpace(reply.Text) == "" {
			return
		}

		// Chunk at Discord's 2000-character message limit. Break on first
		// error so we don't spam if the channel is gone or rate-limited.
		for _, chunk := range gateway.ChunkMessage(reply.Text, 2000) {
			if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
				log.Printf("[discord] send error: %v", err)
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
