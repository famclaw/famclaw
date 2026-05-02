// Package identity maps gateway accounts (Telegram, WhatsApp, Discord) to FamClaw users.
// Unknown accounts get an onboarding message instead of reaching the LLM.
package identity

import (
	"fmt"
	"strings"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// User represents a resolved FamClaw user from a gateway account.
type User struct {
	Name string
}

// Store handles gateway account identity resolution.
type Store struct {
	db *store.DB
}

// NewStore creates an identity Store backed by the given database.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// LinkAccount maps a gateway account to a FamClaw user.
// Gateway name is normalized to lowercase.
func (s *Store) LinkAccount(userName, gateway, externalID string) error {
	gw := strings.ToLower(gateway)
	if err := s.db.LinkGatewayAccount(userName, gw, externalID); err != nil {
		return fmt.Errorf("linking account: %w", err)
	}
	return nil
}

// Resolve looks up a FamClaw user by gateway account.
// Returns nil if the account is not registered — NEVER returns a default.
func (s *Store) Resolve(gateway, externalID string) (*User, error) {
	gw := strings.ToLower(gateway)
	userName, err := s.db.ResolveGatewayAccount(gw, externalID)
	if err != nil {
		return nil, fmt.Errorf("resolving identity: %w", err)
	}
	if userName == "" {
		return nil, nil
	}
	return &User{Name: userName}, nil
}

// IsRegistered checks whether a gateway account is linked to any user.
func (s *Store) IsRegistered(gateway, externalID string) bool {
	return s.db.IsGatewayAccountRegistered(strings.ToLower(gateway), externalID)
}

// Unlink removes a gateway account mapping.
func (s *Store) Unlink(gateway, externalID string) error {
	return s.db.UnlinkGatewayAccount(strings.ToLower(gateway), externalID)
}

// UnlinkedUsers returns the family-config users that have no linked
// account for the given gateway. Used during gateway self-registration
// to either auto-link by display name or present a numbered list.
func (s *Store) UnlinkedUsers(cfg *config.Config, gateway string) []config.UserConfig {
	gw := strings.ToLower(gateway)
	var result []config.UserConfig
	for _, user := range cfg.Users {
		if !s.db.HasGatewayAccount(user.Name, gw) {
			result = append(result, user)
		}
	}
	return result
}
