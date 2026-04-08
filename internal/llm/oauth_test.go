package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPKCEChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := pkceChallenge(verifier)
	if challenge == "" {
		t.Error("challenge should not be empty")
	}
	if challenge == verifier {
		t.Error("challenge should differ from verifier")
	}
	// S256 challenge should be base64url encoded, ~43 chars
	if len(challenge) < 40 || len(challenge) > 50 {
		t.Errorf("challenge length = %d, expected ~43", len(challenge))
	}
}

func TestGenerateRandomString(t *testing.T) {
	s1 := generateRandomString(43)
	s2 := generateRandomString(43)
	if len(s1) != 43 {
		t.Errorf("length = %d, want 43", len(s1))
	}
	if s1 == s2 {
		t.Error("two random strings should differ")
	}
}

func TestOAuthFlowAuthorizationURL(t *testing.T) {
	flow := NewOAuthFlow(DefaultOAuthConfig())
	url := flow.AuthorizationURL(8080)

	if url == "" {
		t.Fatal("URL should not be empty")
	}
	tests := []string{
		"client_id=" + DefaultClientID,
		"response_type=code",
		"code_challenge_method=S256",
		"scope=user",
		"redirect_uri=http%3A%2F%2Flocalhost%3A8080",
	}
	for _, want := range tests {
		if !contains(url, want) {
			t.Errorf("URL missing %q:\n  %s", want, url)
		}
	}
}

func TestOAuthTokenExpired(t *testing.T) {
	tests := []struct {
		name    string
		expires time.Time
		want    bool
	}{
		{"future", time.Now().Add(1 * time.Hour), false},
		{"past", time.Now().Add(-1 * time.Hour), true},
		{"within margin", time.Now().Add(30 * time.Second), true}, // 60s margin
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &OAuthToken{ExpiresAt: tt.expires}
			if token.Expired() != tt.want {
				t.Errorf("Expired() = %v, want %v", token.Expired(), tt.want)
			}
		})
	}
}

func TestOAuthStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	store := NewOAuthStore(path, "http://unused", "client-id")

	token := &OAuthToken{
		AccessToken:  "sk-ant-oat01-test",
		RefreshToken: "refresh-test",
		ExpiresIn:    28800,
		ExpiresAt:    time.Now().Add(8 * time.Hour),
		TokenType:    "Bearer",
	}

	// Save
	if err := store.Save("anthropic", token); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File should exist
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("token file not created: %v", err)
	}

	// Load in new store
	store2 := NewOAuthStore(path, "http://unused", "client-id")
	loaded := store2.Load("anthropic")
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.AccessToken != "sk-ant-oat01-test" {
		t.Errorf("access token = %q", loaded.AccessToken)
	}

	// HasToken
	if !store2.HasToken("anthropic") {
		t.Error("HasToken should return true")
	}
	if store2.HasToken("nonexistent") {
		t.Error("HasToken should return false for missing provider")
	}

	// Delete
	store2.Delete("anthropic")
	if store2.HasToken("anthropic") {
		t.Error("HasToken should return false after delete")
	}
}

func TestOAuthStoreGetAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	store := NewOAuthStore(path, "http://unused", "client-id")

	// No token → error
	_, err := store.GetAccessToken(context.Background(), "anthropic")
	if err == nil {
		t.Error("expected error for missing token")
	}

	// Valid token → returns it
	store.Save("anthropic", &OAuthToken{
		AccessToken:  "valid-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	token, err := store.GetAccessToken(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("token = %q, want 'valid-token'", token)
	}
}

func TestOAuthStoreRefresh(t *testing.T) {
	// Mock token endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}

		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"expires_in":    28800,
			"token_type":    "Bearer",
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	store := NewOAuthStore(path, server.URL, "client-id")

	// Save an expired token
	store.Save("anthropic", &OAuthToken{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour), // expired
	})

	// GetAccessToken should trigger refresh
	token, err := store.GetAccessToken(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("GetAccessToken (refresh): %v", err)
	}
	if token != "new-access-token" {
		t.Errorf("token = %q, want 'new-access-token'", token)
	}

	// Verify new refresh token was stored (rotation)
	loaded := store.Load("anthropic")
	if loaded.RefreshToken != "new-refresh-token" {
		t.Errorf("refresh token not rotated: %q", loaded.RefreshToken)
	}
}

func TestNewOAuthClient(t *testing.T) {
	store := NewOAuthStore(filepath.Join(t.TempDir(), "t.json"), "http://unused", "cid")
	client := NewOAuthClient("https://api.anthropic.com/v1", "claude-sonnet-4-5-20250514", store, "anthropic")

	if client.oauthStore == nil {
		t.Error("oauthStore should not be nil")
	}
	if client.betaHeader != AnthropicBetaHeader {
		t.Errorf("betaHeader = %q, want %q", client.betaHeader, AnthropicBetaHeader)
	}
}

func TestDefaultOAuthConfig(t *testing.T) {
	cfg := DefaultOAuthConfig()
	if cfg.ClientID != DefaultClientID {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.AuthURL != DefaultAuthURL {
		t.Errorf("AuthURL = %q", cfg.AuthURL)
	}
	if cfg.TokenURL != DefaultTokenURL {
		t.Errorf("TokenURL = %q", cfg.TokenURL)
	}
}
