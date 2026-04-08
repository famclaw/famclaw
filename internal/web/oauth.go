package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
)

// handleOAuthStart initiates the Anthropic OAuth flow.
// Returns the authorization URL for the wizard to open in a new tab.
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	flow := llm.NewOAuthFlow(llm.DefaultOAuthConfig())

	// Use the server's own port for callback (wizard is already on this host)
	callbackPort := 0 // let OS pick a free port
	authURL := flow.AuthorizationURL(callbackPort)

	// Store the flow for the callback handler
	s.cfgMu.Lock()
	s.oauthFlow = flow
	s.cfgMu.Unlock()

	// Start a goroutine that waits for the flow to complete
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		token, err := flow.Start(ctx)
		if err != nil {
			log.Printf("[oauth] flow failed: %v", err)
			return
		}

		if s.oauthStore != nil {
			if err := s.oauthStore.Save("anthropic", token); err != nil {
				log.Printf("[oauth] failed to save token: %v", err)
				return
			}
			log.Printf("[oauth] Anthropic token saved (expires in %ds)", token.ExpiresIn)
		}
	}()

	jsonOK(w, map[string]string{
		"auth_url": authURL,
		"status":   "started",
	})
}

// handleOAuthCallback receives the OAuth authorization code from the browser redirect.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	flow := s.oauthFlow
	s.cfgMu.RUnlock()

	if flow == nil {
		http.Error(w, "No OAuth flow in progress", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	if state != flow.State() {
		http.Error(w, "State mismatch — possible CSRF attack", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens
	token, err := flow.ExchangeCode(r.Context(), code, 0)
	if err != nil {
		jsonErr(w, fmt.Errorf("token exchange failed: %w", err), http.StatusInternalServerError)
		return
	}

	// Save token
	if s.oauthStore != nil {
		if err := s.oauthStore.Save("anthropic", token); err != nil {
			jsonErr(w, fmt.Errorf("saving token: %w", err), http.StatusInternalServerError)
			return
		}
	}

	// Clear the flow
	s.cfgMu.Lock()
	s.oauthFlow = nil
	s.cfgMu.Unlock()

	// Return success page that auto-closes
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;text-align:center;padding:60px">
<h2 style="color:#6366f1">Signed in successfully!</h2>
<p>You can close this tab and return to FamClaw.</p>
<script>
if (window.opener) { window.opener.postMessage({type:'oauth_complete',provider:'anthropic'}, '*'); }
setTimeout(function(){ window.close(); }, 2000);
</script>
</body></html>`)
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

// jsonOK and jsonErr are defined in settings.go.
