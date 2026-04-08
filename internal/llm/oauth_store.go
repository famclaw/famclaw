package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OAuthStore persists OAuth tokens and handles automatic refresh.
type OAuthStore struct {
	path     string // e.g. ~/.famclaw/oauth-tokens.json
	tokenURL string
	clientID string
	mu       sync.RWMutex
	tokens   map[string]*OAuthToken
}

// NewOAuthStore creates a token store backed by a JSON file.
func NewOAuthStore(path, tokenURL, clientID string) *OAuthStore {
	s := &OAuthStore{
		path:     path,
		tokenURL: tokenURL,
		clientID: clientID,
		tokens:   make(map[string]*OAuthToken),
	}
	s.loadFromDisk()
	return s
}

// Save stores a token for a provider and persists to disk.
func (s *OAuthStore) Save(provider string, token *OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[provider] = token
	return s.writeToDisk()
}

// Load returns the stored token for a provider, or nil.
func (s *OAuthStore) Load(provider string) *OAuthToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokens[provider]
}

// HasToken returns true if a token exists for the provider.
func (s *OAuthStore) HasToken(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.tokens[provider]
	return ok
}

// Delete removes a token for a provider.
func (s *OAuthStore) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, provider)
	return s.writeToDisk()
}

// GetAccessToken returns a valid access token, refreshing if expired.
func (s *OAuthStore) GetAccessToken(ctx context.Context, provider string) (string, error) {
	s.mu.RLock()
	token, ok := s.tokens[provider]
	s.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("no OAuth token for %q — sign in first", provider)
	}

	if !token.Expired() {
		return token.AccessToken, nil
	}

	// Token expired — refresh it
	newToken, err := s.refreshToken(ctx, token)
	if err != nil {
		return "", fmt.Errorf("refreshing OAuth token: %w", err)
	}

	// Atomic save of new token pair (rotation: old refresh token is now invalid)
	s.mu.Lock()
	s.tokens[provider] = newToken
	s.writeToDisk()
	s.mu.Unlock()

	return newToken.AccessToken, nil
}

func (s *OAuthStore) refreshToken(ctx context.Context, token *OAuthToken) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.clientID},
		"refresh_token": {token.RefreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.tokenURL,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, body)
	}

	var newToken OAuthToken
	if err := json.Unmarshal(body, &newToken); err != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}

	newToken.ExpiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)
	return &newToken, nil
}

func (s *OAuthStore) loadFromDisk() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // file doesn't exist yet
	}
	json.Unmarshal(data, &s.tokens)
}

func (s *OAuthStore) writeToDisk() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating token dir: %w", err)
	}
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling tokens: %w", err)
	}
	return os.WriteFile(s.path, data, 0600)
}
