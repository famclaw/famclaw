package discord

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestSendCancellationStopsDiscordRequests(t *testing.T) {
	for _, phase := range []string{"dm", "message"} {
		t.Run(phase, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var dmCalls, messageCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/dm" {
					dmCalls.Add(1)
				} else {
					messageCalls.Add(1)
				}
				if (phase == "dm" && r.URL.Path == "/dm") || (phase == "message" && r.URL.Path != "/dm") {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"fake-dm"}`)
			}))
			defer server.Close()
			oldDM, oldMessages := discordgo.EndpointUserChannels, discordgo.EndpointChannelMessages
			discordgo.EndpointUserChannels = func(string) string { return server.URL + "/dm" }
			discordgo.EndpointChannelMessages = func(string) string { return server.URL + "/messages" }
			defer func() { discordgo.EndpointUserChannels, discordgo.EndpointChannelMessages = oldDM, oldMessages }()
			s, err := discordgo.New("Bot fake-token")
			if err != nil {
				t.Fatal(err)
			}
			s.Client = &http.Client{Timeout: 3 * time.Second}
			b := &Bot{session: s}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			finished := false
			defer func() {
				cancel()
				close(release)
				if !finished {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("send did not finish during cleanup")
					}
				}
			}()
			go func() { done <- b.Send(ctx, "fake-user", strings.Repeat("a", 4001)) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not start")
			}
			cancel()
			select {
			case err := <-done:
				finished = true
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Send error = %v, want context cancellation", err)
				}
			case <-time.After(time.Second):
				t.Error("Send did not return after cancellation")
			}
			if dmCalls.Load() != 1 {
				t.Errorf("DM calls = %d, want 1", dmCalls.Load())
			}
			wantMessages := int32(0)
			if phase == "message" {
				wantMessages = 1
			}
			if got := messageCalls.Load(); got != wantMessages {
				t.Errorf("message calls = %d, want %d", got, wantMessages)
			}
		})
	}
}
