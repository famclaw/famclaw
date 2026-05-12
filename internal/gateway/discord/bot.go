// Package discord provides a Discord gateway for FamClaw using discordgo.
package discord

import (
	"context"
	"log"
	"strings"
	"time"

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

		displayName := m.Author.GlobalName
		if displayName == "" {
			displayName = m.Author.Username
		}

		msg := gateway.Message{
			Gateway:     "discord",
			ExternalID:  m.Author.ID,
			Text:        m.Content,
			DisplayName: displayName,
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
