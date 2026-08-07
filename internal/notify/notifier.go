// Package notify delivers approval notifications to parent users through
// their linked gateway accounts (Telegram, Discord). Approval requests are
// routed via the same chat gateway the parent already uses — no external
// channels (email, SMS, Slack, ntfy) are required.
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
	"errors"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/store"
)

// ErrNoSender is returned by a sendFn when no gateway Sender is registered
// for the requested gateway — e.g. the parent is linked to a gateway that
// isn't enabled. sendToParents checks for this sentinel to emit a clear
// WARNING rather than logging it as a routine delivery error.
var ErrNoSender = errors.New("no sender registered for gateway")

// Notifier sends approval notifications through a single channel.
// Retained for test mocks.
type Notifier interface {
	Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error
	NotifyDecision(ctx context.Context, a *store.Approval) error
}

// MultiNotifier delivers approval requests and decisions to parent users
// via their linked gateway accounts. Instead of external channels (email,
// Slack, SMS, ntfy), it resolves each parent's gateway accounts through the
// identity store and sends through the matching gateway Sender.
type MultiNotifier struct {
	cfg        *config.Config
	identStore *identity.Store
	sendFn     func(ctx context.Context, gateway, chatID, text string) error
}

// NewMultiNotifier creates a MultiNotifier that delivers approval requests
// to parent users via their linked gateway accounts. The sendFn closure
// resolves a gateway name to its Sender (e.g. from the shared
// senderRegistry) and sends the text to the given chat ID.
//
// The sendFn receives the request context so the underlying sender can
// honour cancellation and deadlines mid-send. sendToParents also checks
// ctx.Done() at both the per-parent and per-account loop levels.
func NewMultiNotifier(cfg *config.Config, identStore *identity.Store, sendFn func(ctx context.Context, gateway, chatID, text string) error) *MultiNotifier {
	return &MultiNotifier{
		cfg:        cfg,
		identStore: identStore,
		sendFn:     sendFn,
	}
}

// Notify sends an approval request to all parent users via their linked
// gateway accounts. Each parent's message is delivered through every gateway
// they have linked (Telegram, Discord, …). Returns an aggregated error if
// any deliveries fail.
func (m *MultiNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	return m.sendToParents(ctx, formatApprovalMessage(a, approveURL, denyURL))
}

// NotifyDecision sends an approval decision to all parent users via their
// linked gateway accounts. Returns an aggregated error if any deliveries fail.
func (m *MultiNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	return m.sendToParents(ctx, formatDecisionMessage(a))
}

// sendToParents iterates parent users in the config, resolves each parent's
// linked gateway accounts, and sends text through the matching gateway Sender.
// Honours context cancellation at every loop level and returns an aggregated
// error for any failed deliveries.
func (m *MultiNotifier) sendToParents(ctx context.Context, text string) error {
	var errs []error
	for _, user := range m.cfg.Users {
		if user.Role != "parent" {
			continue
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("notifications aborted: %w", ctx.Err())
		default:
		}
		accounts, err := m.identStore.ListGatewayAccountsByUser(ctx, user.Name)
		if err != nil {
			log.Printf("[notify] listing accounts for %s: %v", user.Name, err)
			errs = append(errs, fmt.Errorf("listing accounts for %s: %w", user.Name, err))
			continue
		}
		for _, acct := range accounts {
			select {
			case <-ctx.Done():
				return fmt.Errorf("notifications aborted: %w", ctx.Err())
			default:
			}
			// sendFn receives ctx so the underlying gateway sender can honour
			// cancellation mid-send (e.g., abort a slow HTTP POST to Telegram).
			if err := m.sendFn(ctx, acct.Gateway, acct.ExternalID, text); err != nil {
				if errors.Is(err, ErrNoSender) {
					log.Printf("[notify] WARNING: no sender registered for gateway %q; notification for %s/%s dropped",
						acct.Gateway, acct.Gateway, acct.ExternalID)
				} else {
					log.Printf("[notify] sending to %s/%s: %v",
						acct.Gateway, acct.ExternalID, redactWebhookURLInError(err))
				}
				errs = append(errs, fmt.Errorf("sending to %s/%s: %w", acct.Gateway, acct.ExternalID, err))
			}
		}
	}
	return errors.Join(errs...)
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
