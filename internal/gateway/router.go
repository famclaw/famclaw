package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/identity"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// pendingRegistration tracks a user who was shown a "which family member
// are you?" numbered list and is expected to reply with a number. Lives
// at most 5 minutes — see cleanExpiredPending.
type pendingRegistration struct {
	gateway     string
	externalID  string
	displayName string
	unlinked    []config.UserConfig
	askedAt     time.Time
}

// ChatFunc is the function signature for LLM chat.
// Matches the shape of agent.Chat but decoupled for testability.
type ChatFunc func(ctx context.Context, user *config.UserConfig, text string) (string, error)

// Router routes inbound gateway messages through the policy pipeline.
// Uses a SessionPool for per-user serial, cross-user concurrent processing.
type Router struct {
	cfg        *config.Config
	identStore *identity.Store
	clf        *classifier.Classifier
	evaluator  *policy.Evaluator
	db         *store.DB
	notifier   *notify.MultiNotifier
	chatFn     ChatFunc
	pool       *SessionPool

	pendingMu   sync.Mutex
	pendingRegs map[string]*pendingRegistration
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
	r := &Router{
		cfg:         cfg,
		identStore:  identStore,
		clf:         clf,
		evaluator:   evaluator,
		db:          db,
		notifier:    notifier,
		chatFn:      chatFn,
		pendingRegs: make(map[string]*pendingRegistration),
	}
	// Session pool dispatches heavy work (classify → policy → LLM) per-user
	r.pool = NewSessionPool(r.process)
	return r
}

// Handle resolves identity (fast), then dispatches to the per-user session pool.
// Returns immediately after identity resolution — heavy work runs per-user serially.
func (r *Router) Handle(ctx context.Context, msg Message) Reply {
	// ── Step 1: Identity resolve (fast, in caller goroutine) ─────────────
	user, err := r.identStore.Resolve(msg.Gateway, msg.ExternalID)
	if err != nil {
		log.Printf("[router] identity error: %v", err)
		return Reply{Text: "Something went wrong. Please try again.", PolicyAction: "error"}
	}
	if user == nil {
		return r.handleUnknownAccount(msg)
	}

	userCfg := r.cfg.GetUser(user.Name)
	if userCfg == nil {
		log.Printf("[router] user %q not in config", user.Name)
		return Reply{Text: identity.UnknownAccountMessage(), PolicyAction: "onboarding"}
	}

	// ── Step 2: Dispatch to per-user session (heavy work serialized per user)
	return r.pool.Dispatch(ctx, user.Name, msg)
}

// process handles the heavy pipeline: classify → policy → LLM.
// Called by the SessionPool — one at a time per user, concurrent across users.
func (r *Router) process(ctx context.Context, msg Message) Reply {
	// Re-resolve identity (needed for userCfg in this goroutine)
	user, _ := r.identStore.Resolve(msg.Gateway, msg.ExternalID)
	if user == nil {
		return Reply{Text: identity.OnboardingMessage(), PolicyAction: "onboarding"}
	}
	userCfg := r.cfg.GetUser(user.Name)
	if userCfg == nil {
		return Reply{Text: identity.UnknownAccountMessage(), PolicyAction: "onboarding"}
	}

	// ── Classify ─────────────────────────────────────────────────────────
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
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "block"); err != nil {
			log.Printf("[gateway][%s] save blocked user message: %v", user.Name, err)
		}
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "block"); err != nil {
			log.Printf("[gateway][%s] save blocked assistant response: %v", user.Name, err)
		}
		return Reply{Text: text, PolicyAction: "block"}

	case "request_approval":
		r.createApproval(ctx, userCfg, string(cat), msg.Text, requestID)
		text := "I've asked a parent to approve this topic for you. They'll get a notification — once they approve, just ask me again!"
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "request_approval"); err != nil {
			log.Printf("[gateway][%s] save approval-pending user message: %v", user.Name, err)
		}
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "request_approval"); err != nil {
			log.Printf("[gateway][%s] save approval-pending assistant response: %v", user.Name, err)
		}
		return Reply{Text: text, PolicyAction: "request_approval"}

	case "pending":
		text := "A parent has already been notified about this request. Once they approve, you can ask me!"
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "user", msg.Text, string(cat), "pending"); err != nil {
			log.Printf("[gateway][%s] save pending user message: %v", user.Name, err)
		}
		if err := r.db.SaveMessage(conversationID(user.Name), user.Name, "assistant", text, string(cat), "pending"); err != nil {
			log.Printf("[gateway][%s] save pending assistant response: %v", user.Name, err)
		}
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

// handleUnknownAccount runs when Resolve returned no user. Three paths:
//   1. The user is replying to a still-fresh "which family member?" prompt
//      → consume the choice via handleRegistrationReply.
//   2. Their platform display name matches an unlinked FamClaw user
//      → auto-link silently, send a friendly "linked!" message.
//   3. Otherwise → if any users remain unlinked, present a numbered list
//      and stash a pendingRegistration; if no unlinked users remain,
//      reject with the private-family message. No createNewUser path —
//      account creation is parents-only by design.
//
// Note: auto-link by display-name match is a deliberately weak auth
// boundary (anyone with a matching first name on Telegram/Discord can
// claim an unlinked account). Mitigated by parents-only-creates-users.
// Stronger auth would need a one-time pairing code from the dashboard.
func (r *Router) handleUnknownAccount(msg Message) Reply {
	r.cleanExpiredPending()
	key := msg.Gateway + ":" + msg.ExternalID

	r.pendingMu.Lock()
	pending := r.pendingRegs[key]
	r.pendingMu.Unlock()

	if pending != nil && time.Since(pending.askedAt) < 5*time.Minute {
		return r.handleRegistrationReply(msg, pending)
	}

	unlinked := r.identStore.UnlinkedUsers(r.cfg, msg.Gateway)

	if msg.DisplayName != "" {
		firstWord := strings.Split(msg.DisplayName, " ")[0]
		for _, u := range unlinked {
			if strings.EqualFold(u.DisplayName, msg.DisplayName) ||
				strings.EqualFold(u.Name, msg.DisplayName) ||
				strings.EqualFold(u.DisplayName, firstWord) {
				if err := r.identStore.LinkAccount(u.Name, msg.Gateway, msg.ExternalID); err != nil {
					log.Printf("[router] auto-link error: %v", err)
					return Reply{
						Text:         "Something went wrong linking your account. Please try again.",
						PolicyAction: "onboarding",
					}
				}
				log.Printf("[router] auto-linked %s/%s → %s (name match)",
					msg.Gateway, msg.ExternalID, u.Name)
				return Reply{
					Text: fmt.Sprintf(
						"Hi %s! I matched your %s profile and linked your account. You can start chatting!",
						u.DisplayName, msg.Gateway),
					PolicyAction: "onboarding",
				}
			}
		}
	}

	if len(unlinked) == 0 {
		return Reply{
			Text: "This bot belongs to a private family. If you're a family member, " +
				"ask a parent to add your account in the FamClaw dashboard.",
			PolicyAction: "onboarding",
		}
	}

	greeting := "Hi"
	if msg.DisplayName != "" {
		greeting = "Hi " + msg.DisplayName
	}

	var options strings.Builder
	fmt.Fprintf(&options, "%s! Which family member are you?\n\n", greeting)
	for i, u := range unlinked {
		fmt.Fprintf(&options, "%d. %s\n", i+1, u.DisplayName)
	}

	r.pendingMu.Lock()
	r.pendingRegs[key] = &pendingRegistration{
		gateway:     msg.Gateway,
		externalID:  msg.ExternalID,
		displayName: msg.DisplayName,
		unlinked:    unlinked,
		askedAt:     time.Now(),
	}
	r.pendingMu.Unlock()

	return Reply{Text: options.String(), PolicyAction: "onboarding"}
}

// handleRegistrationReply parses a numbered-list reply and links the
// chosen unlinked FamClaw user to the platform account.
func (r *Router) handleRegistrationReply(msg Message, pending *pendingRegistration) Reply {
	r.pendingMu.Lock()
	delete(r.pendingRegs, msg.Gateway+":"+msg.ExternalID)
	r.pendingMu.Unlock()

	text := strings.TrimSpace(msg.Text)
	choice, err := strconv.Atoi(text)
	if err != nil || choice < 1 || choice > len(pending.unlinked) {
		return Reply{
			Text: fmt.Sprintf("Please reply with a number between 1 and %d.",
				len(pending.unlinked)),
			PolicyAction: "onboarding",
		}
	}

	chosen := pending.unlinked[choice-1]
	if err := r.identStore.LinkAccount(chosen.Name, msg.Gateway, msg.ExternalID); err != nil {
		log.Printf("[router] link error: %v", err)
		return Reply{Text: "Something went wrong. Please try again.", PolicyAction: "onboarding"}
	}

	log.Printf("[router] linked %s/%s → %s (user choice)",
		msg.Gateway, msg.ExternalID, chosen.Name)
	return Reply{
		Text: fmt.Sprintf(
			"Welcome, %s! Your account is now linked. You can start chatting!",
			chosen.DisplayName),
		PolicyAction: "onboarding",
	}
}

// cleanExpiredPending drops any pendingRegistration older than 5 minutes.
// Called at the top of every unknown-account flow so the map can't grow
// unboundedly if a user starts the flow and walks away.
func (r *Router) cleanExpiredPending() {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	now := time.Now()
	for key, p := range r.pendingRegs {
		if now.Sub(p.askedAt) > 5*time.Minute {
			delete(r.pendingRegs, key)
		}
	}
}
