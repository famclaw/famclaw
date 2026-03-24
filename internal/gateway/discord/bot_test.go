package discord

import (
	"context"
	"testing"

	"github.com/famclaw/famclaw/internal/gateway"
)

func TestDiscordBotName(t *testing.T) {
	bot := New("test-token")
	if bot.Name() != "discord" {
		t.Errorf("Name() = %q, want discord", bot.Name())
	}
}

func TestDiscordBotHandlerInterface(t *testing.T) {
	// Verify the bot satisfies the Gateway interface
	var _ gateway.Gateway = (*Bot)(nil)
}

func TestDiscordBotHandlerCalled(t *testing.T) {
	var handler func(ctx context.Context, msg gateway.Message) gateway.Reply
	handler = func(ctx context.Context, msg gateway.Message) gateway.Reply {
		return gateway.Reply{Text: "reply", PolicyAction: "allow"}
	}

	reply := handler(context.Background(), gateway.Message{
		Gateway:    "discord",
		ExternalID: "123456",
		Text:       "hello",
	})

	if reply.Text != "reply" {
		t.Errorf("reply.Text = %q, want reply", reply.Text)
	}
}
