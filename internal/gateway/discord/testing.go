package discord

import (
	"github.com/bwmarrin/discordgo"

	"github.com/famclaw/famclaw/internal/gateway"
)

// SendChunked sends text to channelID via the given Discord session,
// splitting at Discord's 2000-character limit. Mirrors the chunking the
// Start() handler performs but is callable directly from tests so the
// WebSocket gateway connection does not need to be stood up.
//
// Tests typically pair this with overriding discordgo.EndpointChannelMessages
// to point at an httptest.Server.
func SendChunked(s *discordgo.Session, channelID, text string) error {
	for _, chunk := range gateway.ChunkMessage(text, 2000) {
		if _, err := s.ChannelMessageSend(channelID, chunk); err != nil {
			return err
		}
	}
	return nil
}
