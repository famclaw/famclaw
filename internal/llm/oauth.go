package llm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultClientID is Claude Code's public OAuth client ID.
	DefaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	DefaultAuthURL  = "https://claude.ai/oauth/authorize"
	DefaultTokenURL = "https://console.anthropic.com/v1/oauth/token"
	DefaultScopes   = "user:profile user:inference"

	// AnthropicBetaHeader is required when using OAuth tokens.
	AnthropicBetaHeader = "oauth-2025-04-20,claude-code-20250219"

	// ClaudeCodeSystemPrefix must be prepended to system prompts when using OAuth.
	ClaudeCodeSystemPrefix = "You are Claude Code, Anthropic's official CLI for Claude."
)

// OAuthConfig holds Anthropic OAuth settings.
type OAuthConfig struct {
	ClientID     string
	AuthURL      string
	TokenURL     string
	Scopes       string
	CallbackPort int // 0 = random available port
}

// DefaultOAuthConfig returns the default config using Claude Code's client ID.
func DefaultOAuthConfig() OAuthConfig {
	return OAuthConfig{
		ClientID: DefaultClientID,
		AuthURL:  DefaultAuthURL,
		TokenURL: DefaultTokenURL,
		Scopes:   DefaultScopes,
	}
}

// OAuthToken holds the access and refresh token pair.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
}

// Expired returns true if the access token has expired or will expire within 60 seconds.
func (t *OAuthToken) Expired() bool {
	return time.Now().After(t.ExpiresAt.Add(-60 * time.Second))
}

type oauthResult struct {
	code string
	err  error
}

// OAuthFlow manages the browser-based OAuth PKCE login.
type OAuthFlow struct {
	cfg      OAuthConfig
	verifier string
	state    string
	result   chan oauthResult
}

// NewOAuthFlow creates a new OAuth flow.
func NewOAuthFlow(cfg OAuthConfig) *OAuthFlow {
	return &OAuthFlow{
		cfg:    cfg,
		result: make(chan oauthResult, 1),
	}
}

// Start initiates the OAuth flow: starts a local server, opens the browser,
// waits for the callback, and exchanges the code for tokens.
func (f *OAuthFlow) Start(ctx context.Context) (*OAuthToken, error) {
	// Generate PKCE verifier and challenge
	f.verifier = generateRandomString(64)
	challenge := pkceChallenge(f.verifier)
	f.state = generateRandomString(43)

	// Start local callback server
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.cfg.CallbackPort))
	if err != nil {
		return nil, fmt.Errorf("starting callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", f.callbackHandler)
	server := &http.Server{Handler: mux}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			f.result <- oauthResult{err: fmt.Errorf("callback server: %w", err)}
		}
	}()
	defer server.Shutdown(context.Background())

	// Build authorization URL
	authURL := f.authorizationURL(port, challenge)
	log.Printf("[oauth] Open this URL to sign in:\n  %s", authURL)

	// Wait for callback or context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-f.result:
		if result.err != nil {
			return nil, result.err
		}
		return f.exchangeCode(ctx, result.code, port)
	}
}

// AuthorizationURL returns the URL the user should open in their browser.
// Exported for use by the web API (wizard opens this in a new tab).
func (f *OAuthFlow) AuthorizationURL(callbackPort int) string {
	f.verifier = generateRandomString(64)
	f.state = generateRandomString(43)
	return f.authorizationURL(callbackPort, pkceChallenge(f.verifier))
}

func (f *OAuthFlow) authorizationURL(port int, challenge string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.cfg.ClientID},
		"redirect_uri":          {fmt.Sprintf("http://localhost:%d/callback", port)},
		"scope":                 {f.cfg.Scopes},
		"state":                 {f.state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return f.cfg.AuthURL + "?" + params.Encode()
}

func (f *OAuthFlow) callbackHandler(w http.ResponseWriter, r *http.Request) {
	// Anthropic returns the code in format: {code}#{state}
	// or as query parameters
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" {
		// Try fragment format
		fragment := r.URL.Query().Get("fragment")
		if fragment != "" {
			parts := strings.SplitN(fragment, "#", 2)
			if len(parts) == 2 {
				code = parts[0]
				state = parts[1]
			}
		}
	}

	if code == "" {
		f.result <- oauthResult{err: fmt.Errorf("no authorization code in callback")}
		http.Error(w, "No authorization code received", http.StatusBadRequest)
		return
	}

	if state != f.state {
		f.result <- oauthResult{err: fmt.Errorf("state mismatch: CSRF protection failed")}
		http.Error(w, "State mismatch", http.StatusBadRequest)
		return
	}

	// Show success page
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><body style="font-family:system-ui;text-align:center;padding:60px">
<h2>Signed in successfully!</h2>
<p>You can close this tab and return to FamClaw.</p>
<script>window.close()</script>
</body></html>`)

	f.result <- oauthResult{code: code}
}

// ExchangeCode exchanges an authorization code for tokens. Exported for web API use.
func (f *OAuthFlow) ExchangeCode(ctx context.Context, code string, callbackPort int) (*OAuthToken, error) {
	return f.exchangeCode(ctx, code, callbackPort)
}

func (f *OAuthFlow) exchangeCode(ctx context.Context, code string, callbackPort int) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {f.cfg.ClientID},
		"code":          {code},
		"redirect_uri":  {fmt.Sprintf("http://localhost:%d/callback", callbackPort)},
		"code_verifier": {f.verifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.cfg.TokenURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, body)
	}

	var token OAuthToken
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	token.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return &token, nil
}

// State returns the current CSRF state value (for external callback validation).
func (f *OAuthFlow) State() string {
	return f.state
}

// Verifier returns the PKCE verifier (needed for code exchange from external callback).
func (f *OAuthFlow) Verifier() string {
	return f.verifier
}

func generateRandomString(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:length]
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
