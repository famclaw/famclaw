package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
)

// oauthState tracks an in-progress OAuth flow. Thread-safe.
type oauthState struct {
	mu       sync.Mutex
	flow     *llm.OAuthFlow
	done     chan struct{}
	err      error
	complete bool
}

// handleOAuthStart initiates the Anthropic OAuth flow.
// The flow owns its own callback listener on a random port.
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	if s.oauthStore == nil {
		jsonErr(w, fmt.Errorf("OAuth is not configured — oauthStore is nil"), http.StatusServiceUnavailable)
		return
	}

	flow := llm.NewOAuthFlow(llm.DefaultOAuthConfig())
	state := &oauthState{
		flow: flow,
		done: make(chan struct{}),
	}

	// Store the state — only one OAuth flow at a time
	s.cfgMu.Lock()
	s.oauthFlow = flow
	s.cfgMu.Unlock()

	// Get the auth URL before Start (which blocks until callback)
	authURL := flow.AuthorizationURL(0) // 0 = let OS pick port

	// Run the full flow in a goroutine — it starts its own callback server
	go func() {
		defer close(state.done)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		token, err := flow.Start(ctx)
		if err != nil {
			state.mu.Lock()
			state.err = err
			state.complete = true
			state.mu.Unlock()
			log.Printf("[oauth] flow failed: %v", err)
			return
		}

		if s.oauthStore != nil {
			if err := s.oauthStore.Save("anthropic", token); err != nil {
				log.Printf("[oauth] failed to save token: %v", err)
			} else {
				log.Printf("[oauth] Anthropic token saved (expires in %ds)", token.ExpiresIn)
			}
		}

		state.mu.Lock()
		state.complete = true
		state.mu.Unlock()
	}()

	jsonOK(w, map[string]string{
		"auth_url": authURL,
		"status":   "started",
	})
}

// handleOAuthCallback is unused — the OAuthFlow owns its own callback server.
// This route exists only as documentation that the callback is handled internally.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "OAuth callback is handled by the internal flow server. This route is not used directly.")
}

// handleOAuthStatus returns whether an Anthropic OAuth token exists.
func (s *Server) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.oauthStore == nil {
		jsonOK(w, map[string]any{"authenticated": false})
		return
	}

	hasToken := s.oauthStore.HasToken("anthropic")
	result := map[string]any{"authenticated": hasToken}

	if hasToken {
		token := s.oauthStore.Load("anthropic")
		if token != nil {
			result["expires_at"] = token.ExpiresAt.Format(time.RFC3339)
			result["expired"] = token.Expired()
		}
	}

	jsonOK(w, result)
}
