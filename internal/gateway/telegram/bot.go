package telegram

import (
	"context"
	"log"
	"strconv"

	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/notify"
	"golang.org/x/time/rate"
	tele "gopkg.in/telebot.v3"
)

// Bot implements the gateway.Gateway interface for Telegram.
type Bot struct {
	bot           *tele.Bot
	handleMsg     func(ctx context.Context, msg gateway.Message) gateway.Reply
	shutdownCtx   context.Context
	shutdownFn    context.CancelFunc
	limiter       *rate.Limiter
	notify        notify.Notifier
}

// NewBot creates a new Telegram bot.
func NewBot(ctx context.Context, token string, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply, limiter *rate.Limiter, notify notify.Notifier) (*Bot, error) {
	bot, err := tele.NewBot(tele.Settings{
		Token: token,
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	return &Bot{
		bot:         bot,
		handleMsg:   handleMsg,
		limiter:     limiter,
		notify:      notify,
		shutdownCtx: ctx,
		shutdownFn:  cancel,
	}, nil
}

// Start begins listening for messages and processing them.
func (b *Bot) Start(ctx context.Context, handleMsg func(ctx context.Context, msg gateway.Message) gateway.Reply) error {
	// Handle messages
	b.bot.Handle(tele.OnText, func(c tele.Context) error {
		// Rate limit by user
		if b.limiter != nil && !b.limiter.Allow() {
			return c.Send("Rate limit exceeded.")
		}

		// Create a new context for this message
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Convert Telegram message to our internal format
		msg := gateway.Message{
			Gateway:     "telegram",
			ExternalID:  strconv.FormatInt(int64(c.Message().ID), 10),
			Text:        c.Message().Text,
			DisplayName: c.Sender().Username,
			GroupID:     strconv.FormatInt(c.Message().Chat.ID, 10),
			IsGroup:     c.Message().Chat.Type == "group" || c.Message().Chat.Type == "supergroup" || c.Message().Chat.Type == "channel",
		}

		// Process the message
		reply := handleMsg(ctx, msg)
		if reply.Text != "" {
			// Send reply
			err := c.Send(reply.Text)
			if err != nil {
				log.Printf("[telegram] error sending reply: %v", err)
			}
		}
		return nil
	})

	// Handle commands
	b.bot.Handle("/start", func(c tele.Context) error {
		return c.Send("Hello! I'm your FamClaw assistant.")
	})

	// Start polling
	go func() {
		b.bot.Start()
	}()

	return nil
}

// Stop shuts down the bot gracefully.
func (b *Bot) Stop() {
	b.shutdownFn()
}

// Name returns the bot's name.
func (b *Bot) Name() string {
	return "telegram"
}
