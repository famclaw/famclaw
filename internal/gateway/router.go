package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// ChatFunc is the function signature for LLM chat.
// Matches the shape of agent.Chat but decoupled for testability.
type ChatFunc func(ctx context.Context, user *config.UserConfig, text string) (string, error)

// Router routes inbound gateway messages through the policy pipeline.
type Router struct {
	cfg        *config.Config
	identStore *identity.Store
	clf        *classifier.Classifier
	evaluator  *policy.Evaluator
	db         *store.DB
	notifier   *notify.MultiNotifier
	chatFn     ChatFunc
}

// NewRouter creates a Router with all required dependencies.
func NewRouter(
	cfg *config.Config,
	identStore *identity.Store,
	clf *classifier.Classifier,
	evaluator *policy.Evaluator,
	db *store.DB,
	notifier *notify.MultiNotifier,
	chatFn ChatFunc,
) *Router {
	return &Router{
		cfg:        cfg,
		identStore: identStore,
		clf:        clf,
		evaluator:  evaluator,
		db:         db,
		notifier:   notifier,
		chatFn:     chatFn,
	}
}

// Handle processes an inbound message through the full pipeline:
//
//	identity.Resolve → classifier.Classify → policy.Evaluate → agent.Chat
func (r *Router) Handle(ctx context.Context, msg Message) Reply {
	// ── Step 1: Identity resolve ─────────────────────────────────────────
	user, err := r.identStore.Resolve(msg.Gateway, msg.ExternalID)
	if err != nil {
		log.Printf("[router] identity error: %v", err)
		return Reply{Text: "Something went wrong. Please try again.", PolicyAction: "error"}
	}
	if user == nil {
		log.Printf("[router] unknown account: %s/%s", msg.Gateway, msg.ExternalID)
		return Reply{Text: identity.OnboardingMessage(), PolicyAction: "onboarding"}
	}

	userCfg := r.cfg.GetUser(user.Name)
	if userCfg == nil {
		log.Printf("[router] user %q not in config", user.Name)
		return Reply{Text: identity.UnknownAccountMessage(), PolicyAction: "onboarding"}
	}

	// ── Step 2: Classify ─────────────────────────────────────────────────
	cat := r.clf.Classify(msg.Text)
	log.Printf("[router] %s/%s user=%s cat=%s", msg.Gateway, msg.ExternalID, user.Name, cat)

	// ── Step 3: Policy evaluate ──────────────────────────────────────────
	requestID := approvalID(user.Name, string(cat))
	approvals, _ := r.db.AllApprovalsForOPA()

	decision, err := r.evaluator.Evaluate(ctx, policy.Input{
		User: policy.UserInput{
			Role:     userCfg.Role,
			AgeGroup: userCfg.AgeGroup,
			Name:     userCfg.Name,
		},
		Query:     policy.QueryInput{Category: string(cat), Text: msg.Text},
		RequestID: requestID,
		Approvals: approvals,
	})
	if err != nil {
		log.Printf("[router] policy error: %v", err)
		return Reply{Text: "Policy evaluation error. Please try again.", PolicyAction: "error"}
	}

	// ── Step 4: Handle non-allow decisions (LLM is NEVER called) ─────────
	switch decision.Action {
	case "block":
		text := fmt.Sprintf("I'm sorry, I can't help with that topic. %s", decision.Reason)
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "block")
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "block")
		return Reply{Text: text, PolicyAction: "block"}

	case "request_approval":
		r.createApproval(ctx, userCfg, string(cat), msg.Text, requestID)
		text := "I've asked a parent to approve this topic for you. They'll get a notification — once they approve, just ask me again!"
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "request_approval")
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "request_approval")
		return Reply{Text: text, PolicyAction: "request_approval"}

	case "pending":
		text := "A parent has already been notified about this request. Once they approve, you can ask me!"
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "pending")
		_ = r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "pending")
		return Reply{Text: text, PolicyAction: "pending"}
	}

	// ── Step 5: LLM chat (only reached when policy returns "allow") ──────
	response, err := r.chatFn(ctx, userCfg, msg.Text)
	if err != nil {
		log.Printf("[router] chat error: %v", err)
		return Reply{Text: "I had trouble thinking of a response. Try again?", PolicyAction: "error"}
	}

	return Reply{Text: response, PolicyAction: "allow"}
}

func (r *Router) createApproval(ctx context.Context, user *config.UserConfig, category, queryText, requestID string) {
	a := &store.Approval{
		ID:          requestID,
		UserName:    user.Name,
		UserDisplay: user.DisplayName,
		AgeGroup:    user.AgeGroup,
		Category:    category,
		QueryText:   queryText,
	}
	isNew, err := r.db.UpsertApproval(a)
	if err != nil {
		log.Printf("[router] approval upsert: %v", err)
		return
	}
	if isNew && r.notifier != nil {
		baseURL := r.cfg.Server.BaseURL()
		approveURL := fmt.Sprintf("%s/decide?id=%s&action=approve&token=%s",
			baseURL, a.ID, notify.GenerateToken(a.ID, "approve", r.cfg.Server.Secret))
		denyURL := fmt.Sprintf("%s/decide?id=%s&action=deny&token=%s",
			baseURL, a.ID, notify.GenerateToken(a.ID, "deny", r.cfg.Server.Secret))
		r.notifier.Notify(ctx, a, approveURL, denyURL)
	}
}

func approvalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}

func conversationID(userName string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + day))
	return hex.EncodeToString(h[:8])
}

// StartAll starts all enabled gateway bots as goroutines.
// Panics are recovered. Failed gateways restart with exponential backoff.
func StartAll(ctx context.Context, gateways []Gateway, handler func(ctx context.Context, msg Message) Reply) {
	for _, gw := range gateways {
		go runGateway(ctx, gw, handler)
	}
}

func runGateway(ctx context.Context, gw Gateway, handler func(ctx context.Context, msg Message) Reply) {
	backoff := time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		startedAt := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[gateway] %s PANIC (recovered): %v", gw.Name(), r)
				}
			}()
			log.Printf("[gateway] starting %s", gw.Name())
			if err := gw.Start(ctx, handler); err != nil {
				if ctx.Err() != nil {
					return // context cancelled, normal shutdown
				}
				log.Printf("[gateway] %s stopped: %v — restarting in %v", gw.Name(), err, backoff)
			}
		}()

		// Reset backoff if gateway ran successfully for > 5 minutes
		if time.Since(startedAt) > 5*time.Minute {
			backoff = time.Second
		}

		// Don't restart if context is done
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Exponential backoff: 1s → 2s → 4s → ... → 60s
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
