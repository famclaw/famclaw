// Package discord provides a Discord gateway for FamClaw using discordgo.
package discord

import (
	"context"
	"log"

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

		if _, err := s.ChannelMessageSend(m.ChannelID, reply.Text); err != nil {
			log.Printf("[discord] send error: %v", err)
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
