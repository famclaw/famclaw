// Package notify provides multi-channel parent notifications for FamClaw.
// When a child's message requires parental approval, notifications are sent
// through all enabled channels (email, Slack, Discord, SMS, ntfy).
package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// Notifier sends approval notifications through a single channel.
type Notifier interface {
	Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error
	NotifyDecision(ctx context.Context, a *store.Approval) error
}

// MultiNotifier dispatches notifications to all enabled channels concurrently.
type MultiNotifier struct {
	channels []Notifier
}

// NewMultiNotifier creates a MultiNotifier from the notification config.
func NewMultiNotifier(cfg config.NotificationsConfig, secret string) *MultiNotifier {
	var channels []Notifier

	if cfg.Email.Enabled {
		channels = append(channels, NewEmailNotifier(cfg.Email))
	}
	if cfg.Slack.Enabled {
		channels = append(channels, NewSlackNotifier(cfg.Slack))
	}
	if cfg.Discord.Enabled {
		channels = append(channels, NewDiscordNotifier(cfg.Discord))
	}
	if cfg.SMS.Enabled {
		channels = append(channels, NewSMSNotifier(cfg.SMS))
	}
	if cfg.Ntfy.Enabled {
		channels = append(channels, NewNtfyNotifier(cfg.Ntfy))
	}

	return &MultiNotifier{channels: channels}
}

// Notify sends approval request to all channels concurrently.
func (m *MultiNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) {
	if len(m.channels) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, ch := range m.channels {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			if err := n.Notify(ctx, a, approveURL, denyURL); err != nil {
				log.Printf("[notify] channel error: %v", err)
			}
		}(ch)
	}
	wg.Wait()
}

// NotifyDecision sends decision notification to all channels concurrently.
func (m *MultiNotifier) NotifyDecision(ctx context.Context, a *store.Approval) {
	if len(m.channels) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, ch := range m.channels {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			if err := n.NotifyDecision(ctx, a); err != nil {
				log.Printf("[notify] decision channel error: %v", err)
			}
		}(ch)
	}
	wg.Wait()
}

// GenerateToken creates a time-limited HMAC token for one-click approve/deny links.
// Format: base64url(id:action:issuedUnix:hmac_hex)
// Token expiry is verified in VerifyToken without a DB lookup.
func GenerateToken(id, action, secret string) string {
	issuedAt := time.Now().Unix()
	payload := fmt.Sprintf("%s:%s:%d", id, action, issuedAt)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	raw := fmt.Sprintf("%s:%s", payload, sig)
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// VerifyToken checks the HMAC signature and expiry of a token.
// Expiry is checked from the timestamp embedded in the token — no DB lookup needed.
// Returns the approval ID and action on success.
func VerifyToken(token, secret string, expiryHours int) (id, action string, err error) {
	decoded, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return "", "", fmt.Errorf("invalid token encoding")
	}
	parts := strings.SplitN(string(decoded), ":", 4)
	if len(parts) != 4 {
		return "", "", fmt.Errorf("invalid token format")
	}
	id, action, issuedStr, sigHex := parts[0], parts[1], parts[2], parts[3]

	issuedUnix, err := strconv.ParseInt(issuedStr, 10, 64)
	if err != nil {
		return "", "", fmt.Errorf("invalid token timestamp")
	}
	if time.Now().Unix() > issuedUnix+int64(expiryHours)*3600 {
		return "", "", fmt.Errorf("token expired")
	}

	payload := fmt.Sprintf("%s:%s:%s", id, action, issuedStr)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sigHex), []byte(expected)) {
		return "", "", fmt.Errorf("invalid token signature")
	}
	return id, action, nil
}

func formatApprovalMessage(a *store.Approval, approveURL, denyURL string) string {
	return fmt.Sprintf(
		"FamClaw Approval Request\n\n"+
			"%s (%s, %s) wants to ask about: %s\n"+
			"Category: %s\n"+
			"Question: %s\n\n"+
			"Approve: %s\n"+
			"Deny: %s",
		a.UserDisplay, a.AgeGroup, a.UserName,
		a.Category, a.Category, a.QueryText,
		approveURL, denyURL,
	)
}

func formatDecisionMessage(a *store.Approval) string {
	return fmt.Sprintf(
		"FamClaw: %s's request about %q has been %s by %s.",
		a.UserDisplay, a.Category, a.Status, a.DecidedBy,
	)
}
