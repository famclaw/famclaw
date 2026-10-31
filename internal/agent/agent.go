// Package agent implements the FamClaw conversation loop.
// Each user gets an isolated conversation with policy enforcement at every turn.
// Delegates to agentcore.Pipeline for the actual processing stages.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/compress"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/convmem"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/filesend"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/prompt"
	"github.com/famclaw/famclaw/internal/reminder"
	"github.com/famclaw/famclaw/internal/sendmsg"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/todo"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/usermemory"
	"github.com/famclaw/famclaw/internal/webfetch"
	"github.com/famclaw/famclaw/internal/webfilter"
	"github.com/famclaw/famclaw/internal/websearch"
)

// Response is the result of a single agent turn.
type Response struct {
	Content      string
	PolicyAction string // allow | block | request_approval | pending
	Category     string
	Streamed     bool
}

// Agent handles a single user's conversation with policy enforcement.
type Agent struct {
	user         *config.UserConfig
	cfg          *config.Config
	llmClient    llm.Chatter
	evaluator    *policy.Evaluator
	classifier   *classifier.Classifier
	db           *store.DB
	pool         *mcp.Pool
	skills       []*skillbridge.Skill
	quarantine   *skillbridge.Quarantine
	scanner      skillbridge.Scanner
	scheduler    *subagent.Scheduler
	builtinTools []agentcore.Tool // tools to inject onto every Turn
	convID       string
	auditGateway string // construction-time gateway for audit-log context; the per-message authority for SaveMessage is msgContext.Gateway (see gatewayForSave)

	// familyState is the always-injected family-state store used at
	// prompt-build time. Phase 3.3 — nil disables family-state injection
	// (tests / legacy callers).
	familyState *familystate.Store

	// userMemory is the per-user memory store used at prompt-build time.
	// Phase 4 — nil disables user-memory injection (tests / legacy callers).
	userMemory *usermemory.Store

	// webFetcher is the function used by handleWebFetch. Defaults to
	// webfetch.Fetch in NewAgent. Tests can swap in a stub to assert the
	// allowlist/scheme/empty-list gates without making real HTTP calls.
	webFetcher func(ctx context.Context, rawURL string, opts webfetch.Options) (*webfetch.Result, error)

	// suspiciousDetector runs the W4 suspicious-site heuristics in
	// hostDecision. Set once in NewAgent (shared, stateless — the detector
	// holds no per-request state, so a single instance is safe to reuse
	// across goroutines). nil falls back to a fresh instance for tests /
	// legacy Agent literals.
	suspiciousDetector *webfilter.SuspiciousSiteDetector
	// suspiciousApprovalOnce guards the nil-fallback allocation of
	// suspiciousDetector for legacy Agent literals (tests). NewAgent always
	// sets suspiciousDetector, so in production this Once never fires and
	// the field is written exactly once, at construction.
	suspiciousApprovalOnce sync.Once
	// baselineStore is the bundled kid-safe blocklist, injected at
	// construction so webfilter keeps no package-level global state.
	baselineStore *webfilter.BaselineStore
	// baselineStoreOnce guards the nil-fallback allocation of baselineStore
	// for legacy Agent literals (tests). NewAgent always sets baselineStore,
	// so in production this Once never fires and the field is written
	// exactly once, at construction. Per-Agent, so webfilter keeps no
	// package-level global state.
	baselineStoreOnce sync.Once
	// onNewSuspiciousApproval — see AgentDeps.OnNewSuspiciousApproval.
	// Set once in NewAgent; nil disables notification.
	onNewSuspiciousApproval func(ctx context.Context, a *store.Approval) error

	// cache is the tool-result spillover store. When non-nil, large
	// handleWebFetch (and other large-result) tool replies are written
	// to the cache and only a budget-sized head slice is returned to the
	// LLM. When nil, the legacy inline-everything path runs.
	cache *toolcache.Cache

	// browserPool drives the builtin__browser_* tools when configured.
	// Typed as the BrowserFetcher interface (not the concrete *browser.Pool)
	// so the web_fetch 403/429 browser-fallback path is testable with a
	// fake (see browser_fallback_test.go); *browser.Pool satisfies it.
	// nil = browser tools disabled.
	browserPool BrowserFetcher

	// todoStore is the per-user todo store.
	todoStore *todo.Store

	// msgContext holds the gateway-specific context for the current message
	// (gateway, external_id, group_id, is_group). Used by outbound tools
	// like reminders to know where to send the notification.
	msgContext gateway.MsgContext
	// transcriber converts audio attachments into text at the top of Chat
	// so the transcript enters the policy pipeline as Turn.Input (before
	// SaveMessage and before any gate reads the input). nil disables voice
	// transcription — audio attachments then produce a visible "voice
	// isn't available" reply rather than a silent drop.
	transcriber          Transcriber
	effectiveSandboxRoot string

	// senderRegistry maps gateway names to their respective sender implementations;
	// fileSenderRegistry maps gateway names to file-delivery implementations.
	senderRegistry     map[string]gateway.Sender
	fileSenderRegistry map[string]gateway.FileSender
	senderRegistryMu   sync.RWMutex // protects senderRegistry and fileSenderRegistry access

	// nowFn returns the current time. Defaults to time.Now; injectable in tests
	// to make research-status timestamps (incl. timeout recording) deterministic.
	nowFn func() time.Time

	// configPath is the on-disk path to config.yaml, used by mcp_add to
	// persist new MCP server entries. Empty string = no persistence.
	configPath string

	// reasoningCache is the shared gateway reasoning auto-detect cache,
	// attached to LLM clients this agent builds (nil = heuristic only).
	reasoningCache *llm.ReasoningCache

	// lifetimeCtx is the server-lifetime context the subagent's timeout
	// context is derived from. It is cancelled on graceful shutdown so
	// in-flight research subagents are terminated deterministically rather
	// than outliving the process and losing their result. When nil
	// (tests), context.Background() is used as the root.
	lifetimeCtx context.Context
}

// Transcriber converts audio attachments into text. The concrete
// implementation lives in the internal/transcribe package; the agent
// depends only on this interface for testability.
type Transcriber interface {
	TranscribeAudio(ctx context.Context, audioData []byte, mimeType string) (string, error)
}

// voiceUnavailableMsg is returned to the user when an audio attachment
// cannot be transcribed (unconfigured or failed). Never a silent drop.
// This is a thin alias of gateway.VoiceUnavailableMsg so the router and
// agent speak the same message; the router is the primary owner of
// voice transcription (it runs before the policy gates).
const voiceUnavailableMsg = gateway.VoiceUnavailableMsg

// AgentDeps holds optional dependencies for an Agent. All fields are
// safe to leave nil — the Agent degrades gracefully (no MCP tools,
// no skills, no scanning, no subagents).
type AgentDeps struct {
	Pool               *mcp.Pool
	Skills             []*skillbridge.Skill
	Quarantine         *skillbridge.Quarantine
	Scanner            skillbridge.Scanner
	Scheduler          *subagent.Scheduler
	BuiltinTools       []agentcore.Tool
	Gateway            string                        // gateway name (telegram, discord, web, etc.) for audit logs
	Cache              *toolcache.Cache              // tool-result spillover cache; nil disables spillover (legacy inline path)
	BrowserPool        *browser.Pool                 // backs builtin__browser_*; nil disables browser tools
	MsgContext         gateway.MsgContext            // gateway-specific context for outbound tools (reminders, etc.)
	SenderRegistry     map[string]gateway.Sender     // map of gateway name (e.g., "telegram", "discord") to Sender implementation
	FileSenderRegistry map[string]gateway.FileSender // map of gateway name to FileSender (file delivery; Discord today)
	Transcriber        Transcriber                   // transcribes audio attachments into text; nil disables voice transcription
	ConfigPath         string                        // on-disk path to config.yaml for mcp_add persistence

	// ReasoningCache supplies the gateway's per-model
	// merge_reasoning_content_in_choices to LLM clients this agent builds.
	// Nil disables auto-detection (heuristic only).
	ReasoningCache *llm.ReasoningCache

	// NowFn, when non-nil, is used to timestamp research status records.
	// Optional — defaults to time.Now.
	NowFn func() time.Time

	// OnNewSuspiciousApproval is called from hostDecision when a NEW
	// suspicious-site approval row is created for a host the detector
	// flagged ask_parent (web_fetch path). Wire it up to deliver the
	// parent push notification — internal/web/server.go does exactly that,
	// mirroring the gateway router's createApproval (kid-only
	// parent-skip, /decide HMAC token URLs). nil (gateway-router agents
	// and tests) skips notification; the row still surfaces on the
	// dashboard.
	OnNewSuspiciousApproval func(ctx context.Context, a *store.Approval) error

	// LifetimeCtx is the server-lifetime context. The subagent's timeout
	// context is derived from it so that graceful shutdown cancels in-flight
	// research deterministically (the result is then recorded as Failed).
	// When nil, context.Background() is used as the root.
	LifetimeCtx context.Context
}

// NewAgent creates an Agent for the given user. Optional dependencies
// (MCP pool, skills, scanner, scheduler, etc.) are passed in deps —
// any field left as zero value disables that capability for this Agent.
// sanitizeDirName returns a string safe to use as a directory name component.
// It uses a strict allowlist approach: only [A-Za-z0-9._-] characters are permitted.
func sanitizeDirName(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("identity cannot be empty")
	}

	// Strict allowlist: only [A-Za-z0-9._-] characters are permitted
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return "", fmt.Errorf("identity %q contains disallowed characters", s)
		}
	}

	// Disallow . and .. after replacement.
	if s == "." || s == ".." {
		return "", fmt.Errorf("identity %q is reserved after sanitization", s)
	}
	if len(s) > 255 {
		return "", fmt.Errorf("identity %q exceeds 255 characters", s)
	}
	return s, nil
}

// computeEffectiveSandboxRoot returns the sandbox root to use for the given
// message context, based on the configured base sandbox root and sandbox scope.
func computeEffectiveSandboxRoot(cfg *config.Config, msgCtx gateway.MsgContext) (string, error) {
	base := cfg.Tools.SandboxRoot
	if base == "" {
		return "", fmt.Errorf("sandbox root not configured")
	}
	scope := cfg.Tools.SandboxScope
	if scope == "" {
		scope = "conversation" // default to per-conversation isolation
	}
	switch scope {
	case "global":
		return base, nil
	case "conversation":
		return conversationSandboxRoot(base, msgCtx)
	default:
		return "", fmt.Errorf("invalid sandbox scope %q", scope)
	}
}

// conversationSandboxRoot computes the per-conversation sandbox root from the
// message context. Each DM maps to conversations/<gateway>/<externalID> and
// each group/channel maps to conversations/<gateway>/<groupID>. The gateway
// name, external ID, and group ID are all passed through sanitizeDirName
// (strict [A-Za-z0-9._-] allowlist) so a crafted identity cannot traverse
// out of the sandbox tree. This is the per-conversation partitioning that
// replaces the shelved per-user scope from issue #221: keying includes the
// gateway so the same user on different platforms (e.g. Alice on Telegram
// vs. Alice on Discord) gets separate subtrees, and groups are keyed by
// their group ID rather than by individual members.
func conversationSandboxRoot(base string, msgCtx gateway.MsgContext) (string, error) {
	if msgCtx.Gateway == "" {
		return "", fmt.Errorf("gateway is empty for conversation scope")
	}
	gw, err := sanitizeDirName(msgCtx.Gateway)
	if err != nil {
		return "", fmt.Errorf("sanitizing gateway name: %w", err)
	}
	var id string
	if msgCtx.IsGroup {
		if msgCtx.GroupID == "" {
			return "", fmt.Errorf("group ID is empty for group conversation scope")
		}
		id, err = sanitizeDirName(msgCtx.GroupID)
		if err != nil {
			return "", fmt.Errorf("sanitizing group ID: %w", err)
		}
	} else {
		if msgCtx.ExternalID == "" {
			return "", fmt.Errorf("external ID is empty for DM conversation scope")
		}
		id, err = sanitizeDirName(msgCtx.ExternalID)
		if err != nil {
			return "", fmt.Errorf("sanitizing external ID: %w", err)
		}
	}
	root := filepath.Join(base, "conversations", gw, id)
	// Defense-in-depth: ensure the computed root stays within base even
	// though sanitizeDirName already rejects separators. This is the
	// Go-level containment assertion — no kernel sandbox backs this up
	// (Landlock only covers MCP subprocesses, not the file_* handlers in
	// the main process).
	cleaned := filepath.Clean(root)
	baseClean := filepath.Clean(base)
	if cleaned == baseClean {
		return "", fmt.Errorf("computed sandbox root escaped base: %q", root)
	}
	if !strings.HasPrefix(cleaned, baseClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("computed sandbox root escaped base: %q", root)
	}
	return root, nil
}
func NewAgent(user *config.UserConfig, cfg *config.Config, llmClient llm.Chatter,
	evaluator *policy.Evaluator, clf *classifier.Classifier, db *store.DB,
	deps AgentDeps) (*Agent, error) {

	// Compute conversation ID. The router passes a convID computed via the
	// idle-gap rule; reuse it for consistency. When ConvID is not set
	// (e.g., tests), start a fresh conversation. We deliberately do NOT
	// call db.LastMessage here — NewAgent has no request context and
	// a DB query could produce a stale ID if the agent is reused. Using
	// time.Now() means each fallback invocation gets a unique convID,
	// which is safe: no stale IDs, no mixing of messages across sessions.
	// In production, the router always sets ConvID.
	var convID string
	if deps.MsgContext.ConvID != "" {
		convID = deps.MsgContext.ConvID
	} else {
		convID = store.ConversationID(user.Name, time.Time{}, false, "", time.Now().UTC())
	}

	// Append admin tools for parent users so they are always available
	// without callers needing to wire them individually.
	builtins := deps.BuiltinTools
	if user.Role == "parent" {
		builtins = append(builtins, admin.AllDefinitions()...)
		builtins = append(builtins, sendmsg.Tool())
	}

	var fs *familystate.Store
	var ts *todo.Store
	var um *usermemory.Store
	if db != nil {
		fs = familystate.NewStore(db)
		ts = todo.NewStore(db)
		um = usermemory.NewStore(db)
	}
	// Compute the effective sandbox root based on the message context.
	effectiveSandboxRoot, sbErr := computeEffectiveSandboxRoot(cfg, deps.MsgContext)
	if sbErr != nil {
		log.Printf("computeEffectiveSandboxRoot: %v", sbErr)
		effectiveSandboxRoot = ""
	} else if err := os.MkdirAll(effectiveSandboxRoot, 0o700); err != nil {
		// Under the default "conversation" scope the root is a partition
		// (conversations/<gateway>/<id>) that does not exist yet on the
		// first message of a conversation; confinePath's EvalSymlinks
		// refuses a non-existent root, which would break every file_*
		// tool. Creating it here keeps confinement fail-closed when the
		// filesystem is actually unusable.
		log.Printf("creating sandbox root %q: %v", effectiveSandboxRoot, err)
		effectiveSandboxRoot = ""
	}

	return &Agent{
		user:                    user,
		cfg:                     cfg,
		llmClient:               llmClient,
		evaluator:               evaluator,
		classifier:              clf,
		db:                      db,
		pool:                    deps.Pool,
		skills:                  deps.Skills,
		quarantine:              deps.Quarantine,
		scanner:                 deps.Scanner,
		scheduler:               deps.Scheduler,
		builtinTools:            builtins,
		convID:                  convID,
		auditGateway:            deps.Gateway,
		familyState:             fs,
		todoStore:               ts,
		userMemory:              um,
		webFetcher:              webfetch.Fetch,
		suspiciousDetector:      webfilter.NewSuspiciousSiteDetector(),
		baselineStore:           webfilter.NewBaselineStore(),
		onNewSuspiciousApproval: deps.OnNewSuspiciousApproval,
		cache:                   deps.Cache,
		browserPool:             deps.BrowserPool,
		msgContext:              deps.MsgContext,
		transcriber:             deps.Transcriber,
		effectiveSandboxRoot:    effectiveSandboxRoot,
		senderRegistry:          deps.SenderRegistry,
		fileSenderRegistry:      deps.FileSenderRegistry,
		nowFn:                   deps.NowFn,
		configPath:              deps.ConfigPath,
		lifetimeCtx:             deps.LifetimeCtx,
		reasoningCache:          deps.ReasoningCache,
	}, nil
}

// now returns the current time, using the injectable nowFn when set
// (tests) so research-status timestamps are deterministic.
func (a *Agent) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

// transcribeAttachments transcribes every audio attachment in the
// message context into text. The transcript is combined with any
// existing userMessage (e.g. a caption). Non-audio attachments
// (images) are ignored — they follow the multimodal path. Returns
// ("", nil) when there are no audio attachments, so text-only messages
// are unaffected.
//
// In production the router transcribes audio BEFORE its classifier and
// policy evaluation and strips the audio attachments, so this method is
// a no-op when called through the router. It remains as a fallback for
// direct Chat calls (tests, web API) and as defence in depth: it runs
// before SaveMessage and before the pipeline gates read Turn.Input, so a
// spoken request always gets the same age/approval gating as a typed one.
func (a *Agent) transcribeAttachments(ctx context.Context, attachments []gateway.Attachment, userMessage string) (string, error) {
	var audioAtts []gateway.Attachment
	for _, att := range attachments {
		if att.Type == "audio" {
			audioAtts = append(audioAtts, att)
		}
	}
	if len(audioAtts) == 0 {
		return "", nil
	}
	if a.transcriber == nil {
		return "", fmt.Errorf("voice transcription is not configured")
	}

	parts := make([]string, 0, len(audioAtts)+1)
	if userMessage != "" {
		parts = append(parts, userMessage)
	}
	for _, att := range audioAtts {
		raw, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return "", fmt.Errorf("decoding audio attachment: %w", err)
		}
		transcript, err := a.transcriber.TranscribeAudio(ctx, raw, att.MIMEType)
		if err != nil {
			return "", fmt.Errorf("transcribing voice: %w", err)
		}
		transcript = strings.TrimSpace(transcript)
		if transcript == "" {
			return "", fmt.Errorf("transcription returned empty text")
		}
		parts = append(parts, transcript)
	}
	return strings.Join(parts, "\n"), nil
}

// gatewayOrUnknown returns the gateway name, falling back to "unknown" when
// the value is empty. gateway.MsgContext is a value type, so a zero-valued
// context yields an empty string — not the "unknown" sentinel. Routing every
// SaveMessage call through this helper guarantees MostRecentGatewayForUser
// never returns "", which would cause cross-chat delivery to misroute or
// drop the reply.
func gatewayOrUnknown(gw string) string {
	if gw == "" {
		return "unknown"
	}
	return gw
}

// gatewayForSave returns the per-message gateway to record for a message
// saved by the agent. It uses the gateway the message arrived on
// (a.msgContext.Gateway) and falls back to "unknown" when that is empty.
func (a *Agent) gatewayForSave() string {
	return gatewayOrUnknown(a.msgContext.Gateway)
}

// Chat processes a single user message and returns a Response.
// Delegates to agentcore.FamilyPipeline for the processing stages.
func (a *Agent) Chat(ctx context.Context, userMessage string, onToken func(string)) (*Response, error) {
	// Transcribe inbound audio attachments so the transcript becomes
	// userMessage BEFORE SaveMessage and before the pipeline gates
	// (StageClassify → StagePolicyInput) read Turn.Input. Images are
	// intentionally NOT transcribed — they follow the multimodal path in
	// buildMessages, below the gates.
	if transcript, err := a.transcribeAttachments(ctx, a.msgContext.Attachments, userMessage); err != nil {
		log.Printf("[agent][%s] voice transcription failed: %v", a.user.Name, err)
		return &Response{Content: voiceUnavailableMsg}, nil
	} else if transcript != "" {
		userMessage = transcript
	}

	// Save user message before processing
	if err := a.db.SaveMessage(a.convID, a.user.Name, "user", userMessage, "", "", a.gatewayForSave()); err != nil {
		log.Printf("[agent][%s] save user message: %v", a.user.Name, err)
	}

	// Build conversation context
	history, _ := a.db.GetConversationHistory(a.convID, 20)
	messages := a.buildMessages(ctx, history, userMessage)

	// Build the pipeline turn
	turn := &agentcore.Turn{
		User:  a.user,
		Input: userMessage,
	}

	// Convert messages to agentcore format
	for _, m := range messages {
		turn.Messages = append(turn.Messages, agentcore.Message{
			Role:         m.Role,
			Content:      m.Content,
			ContentParts: m.ContentParts,
		})
	}

	// Resolve LLM profile. Prefer the injected backend (e.g. claude_cli)
	// when provided; otherwise build an HTTP client from the per-user
	// endpoint config.
	clientFactory := func(t *agentcore.Turn) llm.Chatter {
		if a.llmClient != nil {
			return a.llmClient
		}
		ep := a.cfg.LLMEndpointFor(t.User)
		if ep.BaseURL == "" || ep.Model == "" {
			return nil
		}
		return llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey).WithTimeout(ep.Timeout).WithReasoningCache(a.reasoningCache)
	}

	// Populate available tools on the Turn
	// MCP tools
	if a.pool != nil {
		for _, info := range a.pool.ListToolInfos() {
			t := agentcore.Tool{
				Name:        info.Name,
				Description: info.Description,
				InputSchema: info.InputSchema,
				Source:      "mcp",
				ServerName:  info.ServerName,
				ScanTarget:  info.ScanTarget,
			}
			if t.AllowedForRole(a.user.Role) {
				turn.Tools = append(turn.Tools, t)
			}
		}
	}
	// Builtin tools (spawn_agent, web_fetch, etc.)
	for _, t := range a.builtinTools {
		if t.AllowedForRole(a.user.Role) {
			turn.Tools = append(turn.Tools, t)
		}
	}

	// Buffer tokens during streaming so the output gate can evaluate the
	// full response before any token reaches the gateway.
	// This closes the streamed-output-gate bypass: tokens are no longer
	// emitted live; they are collected, gated, then emitted in one burst.
	var bufferedTokens []string

	// Assemble and run the pipeline
	deps := agentcore.FamilyPipelineDeps{
		Classifier:    a.classifier,
		Evaluator:     a.evaluator,
		DB:            a.db,
		Pool:          a.pool,
		ClientFactory: clientFactory,
		Temperature:   a.cfg.LLM.Temperature,
		MaxTokens:     a.cfg.LLM.MaxResponseTokens,
		ContextWindow: a.cfg.LLM.MaxContextTokens,
	}
	if onToken != nil {
		deps.OnToken = func(tok string) {
			bufferedTokens = append(bufferedTokens, tok)
		}
	}

	// Wire builtin tool handler (spawn_agent, web_fetch, etc.)
	if len(a.builtinTools) > 0 {
		deps.BuiltinHandler = a.makeBuiltinHandler()
	}

	// Wire runtime scanning if configured
	if a.cfg.SecCheck.Enabled && a.cfg.SecCheck.RuntimeScan {
		deps.Quarantine = a.quarantine
		deps.Scanner = a.scanner
		deps.RuntimeScan = true
		deps.BlockOnFail = a.cfg.SecCheck.QuarantineOnFail
		deps.NotifyOnBlock = a.cfg.SecCheck.NotifyOnQuarantine
		deps.Paranoia = a.cfg.SecCheck.Paranoia
		if d, err := time.ParseDuration(a.cfg.SecCheck.RescanInterval); err == nil {
			deps.RescanInterval = d
		} else if a.cfg.SecCheck.RescanInterval != "" {
			log.Printf("[agent] invalid seccheck.rescan_interval %q: %v (using default 7d)", a.cfg.SecCheck.RescanInterval, err)
		}
		if d, err := time.ParseDuration(a.cfg.SecCheck.AsyncScanTimeout); err == nil {
			deps.ScanTimeout = d
		} else if a.cfg.SecCheck.AsyncScanTimeout != "" {
			log.Printf("[agent] invalid seccheck.async_scan_timeout %q: %v (using default 60s)", a.cfg.SecCheck.AsyncScanTimeout, err)
		}
		// Persist runtime tool-scan results to seccheck_reports and gate
		// re-scans with the stale-check so a tool is not re-scanned every turn.
		if a.db != nil && deps.Scanner != nil && deps.Scanner.Available() {
			deps.SaveScanFunc = func(scanTarget string, result *honeybadger.ScanResult) {
				reportJSON, jerr := json.Marshal(result)
				if jerr != nil {
					log.Printf("[agent] seccheck: failed to encode report for %q: %v", scanTarget, jerr)
					return
				}
				if err := a.db.SaveSecCheckReport("", scanTarget, "", result.Score(), result.Verdict, result.Reasoning, string(reportJSON)); err != nil {
					log.Printf("[agent] seccheck: failed to persist report for %q: %v", scanTarget, err)
				}
			}
			deps.LastScanFunc = func(scanTarget string) (time.Time, bool) {
				ts, ok, err := a.db.LastSecCheckReport(scanTarget)
				if err != nil {
					log.Printf("[agent] seccheck: last-scan query error for %q: %v", scanTarget, err)
					return time.Time{}, false
				}
				return ts, ok
			}
		}
	}

	pipeline := agentcore.FamilyPipeline(deps)

	err := pipeline.Run(ctx, turn)

	// Handle policy blocks (not a real error — just a non-allow decision)
	if errors.Is(err, agentcore.ErrPolicyBlock) {
		log.Printf("[agent][%s] cat=%s action=%s", a.user.Name, turn.Category, turn.Policy.Action)
		if err := a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), turn.Policy.Action, a.gatewayForSave()); err != nil {
			log.Printf("[agent][%s] save policy-blocked response: %v", a.user.Name, err)
		}
		return &Response{
			Content:      turn.Output,
			PolicyAction: turn.Policy.Action,
			Category:     string(turn.Category),
		}, nil
	}

	if err != nil {
		return nil, err
	}

	if a.evaluator != nil {
		final, allowed, gateErr := EvaluateAndApply(
			ctx, a.evaluator, turn.Output,
			policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup},
			"",
		)
		if gateErr != nil || !allowed {
			if gateErr != nil {
				log.Printf("[agent][%s] output gate error (treating as block): %v", a.user.Name, gateErr)
			}
			blocked := "I'm unable to send this response right now."
			if dbErr := a.db.SaveMessage(a.convID, a.user.Name, "assistant", blocked, string(turn.Category), "block", a.gatewayForSave()); dbErr != nil {
				log.Printf("[agent][%s] save output-gated response: %v", a.user.Name, dbErr)
			}
			return &Response{
				Content:      blocked,
				PolicyAction: "block",
				Category:     string(turn.Category),
			}, nil
		}
		turn.Output = final
		turn.Output = sanitizeModelResponse(turn.Output)
	}

	// Drain buffered tokens after pipeline completes.
	// - If the output gate hard-blocked (output_blocked=true), turn.Output
	//   is already the safe fallback message and we emit nothing.
	// - If the final output differs from what was streamed — the output
	//   gate redacted it, or the LLM client replaced an unusable response
	//   with a fail-soft message after streaming nothing — emit turn.Output
	//   instead of the raw tokens, so no redaction leaks and no reply is
	//   silently dropped.
	// - If the output gate allowed unchanged, emit the individual tokens.
	if onToken != nil {
		raw := strings.Join(bufferedTokens, "")
		if blocked, ok := turn.GetMeta("output_blocked"); ok && blocked.(bool) {
			// Hard-blocked: safe message already in turn.Output, no emit.
		} else if turn.Output != raw {
			// The delivered text is not what streamed: emit the final
			// output, not the raw tokens.
			onToken(turn.Output)
		} else {
			// Allowed (no modification): emit individual buffered tokens.
			for _, tok := range bufferedTokens {
				onToken(tok)
			}
		}
	}

	// Empty-response guard. Local LLMs (Nemotron particularly) occasionally
	// emit an empty assistant message — no text, no tool_calls — when they
	// can't resolve a prompt cleanly. Without this guard, the gateway sends
	// "" to Discord/Telegram and the user sees nothing, indistinguishable
	// from Butler being down. Per roadmap §11 UX commitment "Never silent
	// failure": surface a brief user-readable fallback instead.
	if strings.TrimSpace(turn.Output) == "" && !turn.Streamed {
		log.Printf("[agent][%s] empty response from LLM — substituting fallback (cat=%s)",
			a.user.Name, turn.Category)
		turn.Output = "I'm not sure how to answer that — could you ask again with more specifics, or rephrase what you're looking for?"
	}

	log.Printf("[agent][%s] cat=%s action=allow", a.user.Name, turn.Category)

	// Save assistant response
	if err := a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), "allow", a.gatewayForSave()); err != nil {
		log.Printf("[agent][%s] save assistant response: %v", a.user.Name, err)
	}

	return &Response{
		Content:      turn.Output,
		PolicyAction: "allow",
		Category:     string(turn.Category),
		Streamed:     turn.Streamed,
	}, nil
}

func (a *Agent) makeBuiltinHandler() func(ctx context.Context, name string, args map[string]any) (string, error) {
	return func(ctx context.Context, name string, args map[string]any) (string, error) {
		switch name {
		case "builtin__spawn_agent":
			return a.handleSpawnAgent(ctx, args)
		case "builtin__research_status":
			return a.handleResearchStatus(ctx, args)
		case "builtin__web_fetch":
			return a.handleWebFetch(ctx, args)
		case "builtin__web_search":
			return a.handleWebSearch(ctx, args)
		case "builtin__browser_navigate",
			"builtin__browser_click",
			"builtin__browser_fill",
			"builtin__browser_select",
			"builtin__browser_press_key",
			"builtin__browser_extract",
			"builtin__browser_wait_for",
			"builtin__browser_screenshot",
			"builtin__browser_snapshot",
			"builtin__browser_fill_form",
			"builtin__browser_done":
			return a.handleBrowser(ctx, name, args)
		case "builtin__tool_result_more":
			return a.handleToolResultMore(ctx, args)
		case "builtin__get_family_state":
			return a.handleGetFamilyState(ctx, args)
		case "builtin__propose_family_fact":
			return a.handleProposeFamilyFact(ctx, args)
		case "builtin__todo":
			return a.handleTodo(ctx, args)
		case "builtin__remember_user_memory":
			if a.userMemory == nil {
				return "", fmt.Errorf("user memory not configured")
			}
			category, _ := args["category"].(string)
			label, _ := args["label"].(string)
			value, _ := args["value"].(string)
			return usermemory.HandleRemember(ctx, a.userMemory, a.user.Name, category, label, value)
		case "builtin__recall_user_memory":
			if a.userMemory == nil {
				return "", fmt.Errorf("user memory not configured")
			}
			category, _ := args["category"].(string)
			query, _ := args["query"].(string)
			return usermemory.HandleRecall(ctx, a.userMemory, a.user.Name, category, query)
		case "builtin__forget_user_memory":
			if a.userMemory == nil {
				return "", fmt.Errorf("user memory not configured")
			}
			category, _ := args["category"].(string)
			label, _ := args["label"].(string)
			return usermemory.HandleForget(ctx, a.userMemory, a.user.Name, category, label)
		case "builtin__search_conversations":
			query, _ := args["query"].(string)
			fromDate, _ := args["from_date"].(string)
			toDate, _ := args["to_date"].(string)
			var maxResults int
			if v, ok := args["max_results"].(float64); ok {
				maxResults = int(v)
			}
			return convmem.HandleSearch(ctx, a.db, a.user.Name, query, fromDate, toDate, maxResults)
		case "builtin__list_pending_approvals":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleListPendingApprovals(ctx, deps, args)
		case "builtin__list_users":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleListUsers(ctx, deps, args)
		case "builtin__list_unknown_accounts":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleListUnknownAccounts(ctx, deps, args)
		case "builtin__approve_request":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, FamilyState: a.familyState}
			return admin.HandleApproveRequest(ctx, deps, args)
		case "builtin__deny_request":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleDenyRequest(ctx, deps, args)
		case "builtin__set_user_role":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleSetUserRole(ctx, deps, args)
		case "builtin__link_account":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway}
			return admin.HandleLinkAccount(ctx, deps, args)
		case "builtin__set_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, FamilyState: a.familyState}
			return admin.HandleSetFamilyFact(ctx, deps, args)
		case "builtin__delete_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, FamilyState: a.familyState}
			return admin.HandleDeleteFamilyFact(ctx, deps, args)
		case "builtin__add_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, FamilyState: a.familyState}
			return admin.HandleAddFamilyCategory(ctx, deps, args)
		case "builtin__delete_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, FamilyState: a.familyState}
			return admin.HandleDeleteFamilyCategory(ctx, deps, args)
		case "builtin__file_read":
			return a.handleFileRead(ctx, args)
		case "builtin__file_write":
			return a.handleFileWrite(ctx, args)
		case "builtin__file_stat":
			return a.handleFileStat(ctx, args)
		case "builtin__file_list":
			return a.handleFileList(ctx, args)
		case "builtin__add_reminder":
			when, _ := args["when"].(string)
			message, _ := args["message"].(string)
			forUser, _ := args["for_user"].(string)
			return reminder.HandleAddReminder(ctx, a.db, a.cfg, a.user, a.msgContext.Gateway, a.msgContext.ExternalID, a.msgContext.GroupID, a.msgContext.IsGroup, when, message, forUser)
		case "builtin__send_message":
			to, _ := args["to"].(string)
			msg, _ := args["message"].(string)
			a.senderRegistryMu.RLock()
			senders := make(map[string]gateway.Sender, len(a.senderRegistry))
			for k, v := range a.senderRegistry {
				senders[k] = v
			}
			a.senderRegistryMu.RUnlock()
			return sendmsg.Handle(ctx, a.db, a.cfg, senders, a.user.Name, a.gatewayForSave(), to, msg)
		case "builtin__send_file":
			path, _ := args["path"].(string)
			caption, _ := args["caption"].(string)
			a.senderRegistryMu.RLock()
			fileSenders := make(map[string]gateway.FileSender, len(a.fileSenderRegistry))
			for k, v := range a.fileSenderRegistry {
				fileSenders[k] = v
			}
			a.senderRegistryMu.RUnlock()
			var auditDB filesend.DB
			if a.db != nil {
				auditDB = a.db
			}
			return filesend.Handle(ctx, auditDB, fileSenders, a.user.Name, a.gatewayForSave(), a.msgContext.Gateway, gateway.OutboundDestination{
				ExternalID: a.msgContext.ExternalID,
				GroupID:    a.msgContext.GroupID,
			}, a.effectiveSandboxRoot, path, caption)
		case "builtin__mcp_list":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, MCP: a.pool, ConfigPath: a.configPath, Scanner: a.scanner}
			return admin.HandleMCPList(ctx, deps, args)
		case "builtin__mcp_add":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, MCP: a.pool, ConfigPath: a.configPath, Scanner: a.scanner}
			return admin.HandleMCPAdd(ctx, deps, args)
		case "builtin__mcp_remove":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, MCP: a.pool, ConfigPath: a.configPath}
			return admin.HandleMCPRemove(ctx, deps, args)
		case "builtin__allowlist_list":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleAllowlistList(ctx, deps, args)
		case "builtin__allowlist_add":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleAllowlistAdd(ctx, deps, args)
		case "builtin__allowlist_remove":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleAllowlistRemove(ctx, deps, args)
		case "builtin__denylist_list":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleDenylistList(ctx, deps, args)
		case "builtin__denylist_add":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleDenylistAdd(ctx, deps, args)
		case "builtin__denylist_remove":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.auditGateway, ConfigPath: a.configPath}
			return admin.HandleDenylistRemove(ctx, deps, args)
		default:
			return "", fmt.Errorf("unknown builtin tool: %s", name)
		}
	}
}

func (a *Agent) confinePath(path string) (string, error) {
	// Delegates to the shared containment rule (also used by send_file) so
	// every conversation-file tool enforces identical escape checks.
	return filesend.ConfinePath(a.effectiveSandboxRoot, path)
}

// Subagent timeout defaults / caps (in seconds).
const (
	subagentDefaultTimeoutSec = 300  // 5 minutes
	subagentMaxTimeoutSec     = 1800 // 30 minutes
)

func (a *Agent) handleFileRead(ctx context.Context, args map[string]any) (string, error) {
	pathRaw, ok := args["path"].(string)
	if !ok || pathRaw == "" {
		return "", fmt.Errorf("file_read requires a non-empty 'path' argument")
	}
	absPath, err := a.confinePath(pathRaw)
	if err != nil {
		return "", fmt.Errorf("resolving file_read path: %w", err)
	}
	maxBytes := a.cfg.Tools.FileRead.MaxBytes
	if maxBytes > 0 {
		file, err := os.Open(absPath)
		if err != nil {
			return "", fmt.Errorf("opening file: %w", err)
		}
		defer file.Close()
		data := make([]byte, maxBytes)
		n, err := file.Read(data)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("reading file: %w", err)
		}
		data = data[:n]
		// Check if there is more data
		var buf [1]byte
		_, err = file.Read(buf[:])
		if err == nil {
			// We read an extra byte, meaning there is more data.
			return string(data) + "\n[truncated]", nil
		} else if err != io.EOF {
			return "", fmt.Errorf("checking for extra data: %w", err)
		}
		// If err == io.EOF, then we reached the end.
		return string(data), nil
	} else {
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", fmt.Errorf("reading file: %w", err)
		}
		return string(data), nil
	}
}

func (a *Agent) handleFileWrite(ctx context.Context, args map[string]any) (string, error) {
	pathRaw, ok := args["path"].(string)
	if !ok || pathRaw == "" {
		return "", fmt.Errorf("file_write requires a non-empty 'path' argument")
	}
	contentRaw, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("file_write requires a 'content' argument")
	}
	// Split the path into directory and base.
	dir := filepath.Dir(pathRaw)
	base := filepath.Base(pathRaw)
	if dir == "" {
		dir = "."
	}
	// Confine the directory (must exist). confinePath canonicalises via
	// EvalSymlinks so confinedDir is an absolute, symlink-resolved path
	// already verified to sit inside the sandbox root.
	confinedDir, err := a.confinePath(dir)
	if err != nil {
		return "", fmt.Errorf("resolving file_write path: %w", err)
	}
	// Form the absolute path for the file, then CollapseDotDot via Clean.
	joinedPath := filepath.Clean(filepath.Join(confinedDir, base))
	// Re-check that the joined path is within the sandbox root. confinePath
	// evaluated the directory but not the final component (a non-existent
	// file's symlinks cannot be resolved yet), so a pathological base like
	// ".." can shift joinedPath back outside the root. We also need to
	// defeat a symlink INSIDE the sandbox that points outside — the string
	// prefix check below is not enough on its own; we re-resolve via
	// EvalSymlinks when possible and reconfirm containment.
	if a.effectiveSandboxRoot == "" {
		return "", fmt.Errorf("file_write: sandbox root not configured")
	}
	sandboxRoot, err := filepath.EvalSymlinks(filepath.Clean(a.effectiveSandboxRoot))
	if err != nil {
		return "", fmt.Errorf("file_write: invalid sandbox root: %w", err)
	}
	rel, relErr := filepath.Rel(sandboxRoot, joinedPath)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("file_write path %q escapes sandbox root %q", pathRaw, sandboxRoot)
	}
	// Defence-in-depth against in-sandbox symlinks pointing outside. If
	// the target file already exists, EvalSymlinks returns the resolved
	// path; otherwise (new file) the path either does not exist or its
	// parent has been resolved by confinePath already — so this branch
	// only catches the "the wrong-named symlink is in the way" case.
	if resolved, err := filepath.EvalSymlinks(joinedPath); err == nil {
		if !isWithinDir(resolved, sandboxRoot) {
			return "", fmt.Errorf("file_write path %q resolves to %q outside sandbox", pathRaw, resolved)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("file_write: failed to resolve %q: %w", joinedPath, err)
	}
	// Write the file with restrictive mode (sandbox holds user-private data).
	if err := os.WriteFile(joinedPath, []byte(contentRaw), 0600); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	return fmt.Sprintf("ok — wrote %d bytes to %q", len(contentRaw), pathRaw), nil
}

// isWithinDir reports whether path equals dir or sits beneath it, using
// a separator-aware prefix match. Both arguments must be cleaned first.
// Returns false if path is absolute and outside dir.
func isWithinDir(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, dir+sep)
}

func (a *Agent) handleFileStat(ctx context.Context, args map[string]any) (string, error) {
	pathRaw, ok := args["path"].(string)
	if !ok || pathRaw == "" {
		return "", fmt.Errorf("file_stat requires a non-empty 'path' argument")
	}
	absPath, err := a.confinePath(pathRaw)
	if err != nil {
		return "", fmt.Errorf("resolving file_stat path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("statting file: %w", err)
	}
	return fmt.Sprintf("Name: %s\nSize: %d bytes\nMode: %s\nModTime: %s", info.Name(), info.Size(), info.Mode(), info.ModTime().Format(time.RFC3339)), nil
}

func (a *Agent) handleFileList(ctx context.Context, args map[string]any) (string, error) {
	pathRaw, ok := args["path"].(string)
	if !ok {
		pathRaw = "" // default to sandbox root
	}
	absPath, err := a.confinePath(pathRaw)
	if err != nil {
		return "", fmt.Errorf("resolving file_list path: %w", err)
	}
	files, err := os.ReadDir(absPath)
	if err != nil {
		return "", fmt.Errorf("reading directory: %w", err)
	}
	var lines []string
	var truncated bool
	if maxEntries := a.cfg.Tools.FileList.MaxEntries; maxEntries > 0 {
		if len(files) > maxEntries {
			files = files[:maxEntries]
			truncated = true
		}
	}
	for _, f := range files {
		line := f.Name()
		if f.IsDir() {
			line += "/"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "(empty)", nil
	}
	result := strings.Join(lines, "\n")
	if truncated {
		result += "\n[truncated]"
	}
	return result, nil
}

// handleTodo dispatches the builtin__todo tool. Manages the user's personal todo list.
// Actions: add, list, complete, uncomplete, remove.
func (a *Agent) handleTodo(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return "", fmt.Errorf("todo requires an 'action' argument")
	}

	userName := a.user.Name

	switch action {
	case "add":
		text, _ := args["text"].(string)
		if text == "" {
			return "", fmt.Errorf("todo add requires a 'text' argument")
		}
		t, err := a.todoStore.AddTodo(ctx, userName, text)
		if err != nil {
			return "", fmt.Errorf("add todo: %w", err)
		}
		return fmt.Sprintf("Added todo #%d: %s", t.ID, t.Text), nil

	case "list":
		filter, _ := args["filter"].(string)
		if filter == "" {
			filter = "all" // default to showing all
		}
		var completed *bool
		switch filter {
		case "active":
			f := false
			completed = &f
		case "completed":
			f := true
			completed = &f
		case "all":
			completed = nil
		default:
			return "", fmt.Errorf("invalid filter %q: must be all, active, or completed", filter)
		}
		todos, err := a.todoStore.ListTodos(ctx, userName, completed)
		if err != nil {
			return "", fmt.Errorf("list todos: %w", err)
		}
		if len(todos) == 0 {
			return "No todos found.", nil
		}
		var lines []string
		for _, t := range todos {
			status := " "
			if t.Completed {
				status = "x"
			}
			lines = append(lines, fmt.Sprintf("%d. [%s] %s", t.ID, status, t.Text))
		}
		return strings.Join(lines, "\n"), nil

	case "complete":
		id := readIntArg(args, "id", 0)
		if id == 0 {
			return "", fmt.Errorf("todo complete requires an 'id' argument")
		}
		if err := a.todoStore.CompleteTodo(ctx, userName, int64(id)); err != nil {
			return "", fmt.Errorf("complete todo: %w", err)
		}
		return fmt.Sprintf("Marked todo #%d as complete.", id), nil

	case "uncomplete":
		id := readIntArg(args, "id", 0)
		if id == 0 {
			return "", fmt.Errorf("todo uncomplete requires an 'id' argument")
		}
		if err := a.todoStore.UncompleteTodo(ctx, userName, int64(id)); err != nil {
			return "", fmt.Errorf("uncomplete todo: %w", err)
		}
		return fmt.Sprintf("Marked todo #%d as active.", id), nil

	case "remove":
		id := readIntArg(args, "id", 0)
		if id == 0 {
			return "", fmt.Errorf("todo remove requires an 'id' argument")
		}
		if err := a.todoStore.RemoveTodo(ctx, userName, int64(id)); err != nil {
			return "", fmt.Errorf("remove todo: %w", err)
		}
		return fmt.Sprintf("Removed todo #%d.", id), nil

	default:
		return "", fmt.Errorf("unknown todo action %q (must be add, list, complete, uncomplete, or remove)", action)
	}
}

func (a *Agent) handleSpawnAgent(ctx context.Context, args map[string]any) (string, error) {
	if a.scheduler == nil {
		return "", fmt.Errorf("spawn_agent unavailable: subagent scheduler is not configured")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "", fmt.Errorf("spawn_agent requires a 'prompt' argument")
	}

	profile, _ := args["profile"].(string)
	maxTurns := 10
	if mt, ok := args["max_turns"].(float64); ok && mt > 0 {
		maxTurns = int(mt)
	}
	if maxTurns > 20 {
		maxTurns = 20 // hard cap to prevent runaway subagents
	}

	timeoutSec := normalizeTimeoutSeconds(args)

	allowTools := parseStringList(args["tools"])
	denyTools := parseStringList(args["deny_tools"])
	// Default the subagent to web_fetch when the caller leaves tools
	// empty — otherwise the subagent has zero tools and just answers
	// from training data, which is the silent failure mode we saw
	// before v0.5.9 where the parent agent "delegated research" to a
	// subagent that couldn't actually research anything.
	if len(allowTools) == 0 {
		allowTools = []string{"web_fetch"}
	}

	cfg := subagent.Config{
		Prompt:     prompt,
		LLMProfile: profile,
		MaxTurns:   maxTurns,
		Tools:      allowTools,
		DenyTools:  denyTools,
	}

	// Build the subagent's builtin tool view from this parent's builtins.
	// The parent's makeBuiltinHandler dispatches by namespaced name and
	// already shares state (config, db, user, pool) with the subagent.
	// Subagents inherit the parent's identity but stay read-only: any tool
	// that mutates shared family state is filtered out here.
	builtinDefs := make([]llm.ToolDef, 0, len(a.builtinTools))
	for _, t := range a.builtinTools {
		if isSubagentExcludedTool(t.Name) {
			continue
		}
		builtinDefs = append(builtinDefs, llm.ToolDef{
			Type: "function",
			Function: llm.ToolDefFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	execDeps := subagent.ExecutorDeps{
		Pool:           a.pool,
		Config:         a.cfg,
		Temperature:    a.cfg.LLM.Temperature,
		MaxTokens:      a.cfg.LLM.MaxResponseTokens,
		BuiltinDefs:    builtinDefs,
		BuiltinHandler: a.makeBuiltinHandler(),
		ReasoningCache: a.reasoningCache,
	}

	// The subagent's timeout context is rooted at the server-lifetime context
	// (not the inbound chat-turn ctx, which is cancelled when this handler
	// returns, and not context.Background(), which would ignore shutdown).
	// This means: the chat turn ending does NOT kill the subagent, and
	// graceful shutdown cancels the subagent deterministically — whose
	// result resolveSubagentOutcome then records as Failed with a truthful
	// reason rather than letting the process exit and lose it silently.
	lifetimeCtx := a.lifetimeCtx
	if lifetimeCtx == nil {
		lifetimeCtx = context.Background() // tests
	}
	subCtx, cancel := context.WithTimeout(lifetimeCtx, time.Duration(timeoutSec)*time.Second)

	agentID, resultCh, err := a.scheduler.Submit(subCtx, cfg, func(ctx context.Context, cfg subagent.Config) (string, error) {
		return subagent.Execute(ctx, cfg, execDeps)
	})
	if err != nil {
		cancel()
		// No task id exists yet, so there is nothing to persist; the error is
		// returned to the LLM (not swallowed) so the user sees the failure.
		return "", fmt.Errorf("spawning subagent: %w", err)
	}

	// Record the "running" status so a crash/timeout/delivery failure leaves a
	// queryable trace even if the background goroutine never completes.
	a.persistResearchStart(agentID, cfg.Prompt, timeoutSec, a.msgContext)

	log.Printf("[agent][%s] spawned subagent %s on profile %q (timeout=%ds)", a.user.Name, agentID, profile, timeoutSec)

	// Return immediately with an acknowledgment instead of waiting for result
	ackMsg := fmt.Sprintf("Started your research (task %s). I'll post the result here when it's done; if I can't deliver it, the result is saved and you can check it with: research status %s.", agentID, agentID)

	// Launch background goroutine to handle result delivery. The goroutine
	// uses a bounded finalization context (derived from the lifetime context)
	// so the result is persisted and delivered even after the inbound chat-turn
	// context is gone, but never blocks forever on a wedged DB or gateway.
	go func() {
		msgCtx := a.msgContext
		prompt := cfg.Prompt

		// cancel the subagent's context when the goroutine exits.
		defer cancel()

		finalCtx, finalCancel := context.WithTimeout(lifetimeCtx, finalizeTimeout)
		defer finalCancel()
		state, resultText := resolveSubagentOutcome(resultCh, subCtx)
		a.finalizeResearch(finalCtx, agentID, state, resultText, timeoutSec, prompt, msgCtx)
	}()

	return ackMsg, nil
}

// deliverySendTimeout bounds how long a single async-result delivery Send may
// block before it is abandoned, so a stuck gateway cannot hang the goroutine.
const deliverySendTimeout = 30 * time.Second

// finalizeTimeout bounds how long the research finalizer may take to persist
// the status record and attempt delivery. Long enough for a DB write plus a
// delivery attempt; never unbounded, so a wedged store/gateway cannot block
// the finalizer goroutine indefinitely.
const finalizeTimeout = 30 * time.Second

// deliverResultToOrigin sends a message to the originating conversation.
// It returns whether the message was delivered and any error. A missing sender
// or a failed Send is NOT silently swallowed: the caller (finalizeResearch)
// records the failure in the persistent status and surfaces it to the user.
func (a *Agent) deliverResultToOrigin(agentID string, message string, msgCtx gateway.MsgContext) (bool, error) {
	// Look up the sender by gateway name
	a.senderRegistryMu.RLock()
	sender, ok := a.senderRegistry[msgCtx.Gateway]
	a.senderRegistryMu.RUnlock()

	if !ok {
		err := fmt.Errorf("no sender registered for gateway %q", msgCtx.Gateway)
		log.Printf("[agent][%s] %v for agent %s", a.user.Name, err, agentID)
		return false, err
	}

	// Compute chatID: use GroupID if IsGroup and GroupID is not empty, otherwise use ExternalID
	chatID := msgCtx.ExternalID
	if msgCtx.IsGroup && msgCtx.GroupID != "" {
		chatID = msgCtx.GroupID
	}

	// Send the message
	ctx, cancel := context.WithTimeout(context.Background(), deliverySendTimeout)
	defer cancel()
	if err := sender.Send(ctx, chatID, message); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[agent][%s] Timed out after %s sending result for agent %s to %s", a.user.Name, deliverySendTimeout, agentID, chatID)
			return false, fmt.Errorf("send timed out after %s: %w", deliverySendTimeout, err)
		}
		log.Printf("[agent][%s] Failed to send result for agent %s to %s: %v", a.user.Name, agentID, chatID, err)
		return false, fmt.Errorf("send to %s: %w", chatID, err)
	}
	return true, nil
}

// canonicalizeHost lowercases a host, trims surrounding whitespace and a
// trailing dot, so "EXAMPLE.com." matches "example.com". It is the same
// comparison form the legacy allowlist gates used, so host matching stays
// byte-for-byte unchanged in allowlist mode.
func canonicalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hostDecision is the single source of truth for the web_fetch host gate,
// shared by web_fetch, web_search, the browser-fallback path, and
// browser_navigate (design §1.2 — replaces the duplicated per-call-site
// hostAllowed closures). It branches on the resolved URL policy mode:
//
//	allowlist: allowed iff host matches a url_allowlist entry (exact or
//	            subdomain). An empty allowlist denies all (legacy SSRF guard).
//	denylist:  allowed for any host unless it matches a url_denylist entry
//	            (exact or subdomain).
//
// The scheme gate, redirect re-validation, and the dialer's private-IP vetting
// are orthogonal and remain applied in both modes (design §1.4); hostDecision
// only makes the host-level allow/deny decision. It returns (allowed, reason)
// where reason is a short class ("allowlist" | "denylist") when !allowed.
func (a *Agent) hostDecision(ctx context.Context, host string) (bool, string) {
	host = canonicalizeHost(host)
	fcfg := a.cfg.Tools.WebFetch
	switch fcfg.ResolvedURLPolicy() {
	case "allowlist":
		for _, allowed := range fcfg.URLAllowlist {
			allowed = canonicalizeHost(allowed)
			if allowed == "" {
				continue
			}
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				return true, ""
			}
		}
		return false, "allowlist"
	default: // "denylist"
		// (a) Bundled kid-safe baseline (CC0-sourced, design §2) — always
		// active in denylist mode; reason class "baseline" drives the
		// kid-safe no-leak message (design §3.3).
		// baselineStore is injected in NewAgent; nil falls back to a
		// Once-guarded store for legacy Agent literals in tests (same
		// pattern as the suspiciousDetector fallback below).
		if a.baselineStore == nil {
			a.baselineStoreOnce.Do(func() { a.baselineStore = webfilter.NewBaselineStore() })
		}
		baselineStore := a.baselineStore
		if baselineStore.Blocks(host) {
			return false, "baseline"
		}
		// (b) Parent-maintained deny entries.
		for _, denied := range fcfg.URLDenylist {
			denied = canonicalizeHost(denied)
			if denied == "" {
				continue
			}
			if host == denied || strings.HasSuffix(host, "."+denied) {
				return false, "denylist"
			}
		}

		// (c) W4: host-level suspicion gate (design §4). The detector's
		// heuristics target malicious PUBLIC domain names (punycode lookalikes,
		// high-entropy C2 labels, typo-squats of known brands).
		//
		// IP literals: the home-LAN model (design §1.5) permits private/loopback/
		// link-local endpoints (SearXNG, NAS, local services) by default — raw-IP
		// security for those ranges is the orthogonal dialer gate's job
		// (webfetch block_private_networks). A raw PUBLIC IP literal, however, is
		// not a domain: the domain-based baseline + parent denylist above cannot
		// match an IP, so a child could reach a baseline-blocked site by
		// navigating to its public IP instead of its name. Public IPs are
		// therefore routed to the parent-approval gate (ask_parent) rather than
		// silently allowed.
		//
		// This IP classification intentionally mirrors webfilter's
		// classifyIPLiteral / SuspiciousSiteDetector.Check IP branch (design
		// §1.5) so the agent gate and the detector agree on local vs. public
		// ranges; the agent short-circuits here (early allow for local IPs,
		// ask_parent for public IPs) instead of calling Check. If the local-IP
		// predicate below (IsPrivate/IsLoopback/IsLinkLocalUnicast/
		// IsLinkLocalMulticast) changes, update webfilter.classifyIPLiteral to
		// match.
		var verdict webfilter.Verdict
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return true, ""
			}
			verdict = webfilter.Verdict{Action: "ask_parent", Reason: "public IP literal host"}
		} else {
			// Fallback for legacy Agent literals (tests): allocate once
			// instead of a fresh detector per hostDecision call. NewAgent
			// always sets the field, so in production this Do is a no-op.
			a.suspiciousApprovalOnce.Do(func() { a.suspiciousDetector = webfilter.NewSuspiciousSiteDetector() })
			verdict = a.suspiciousDetector.Check(host)
		}
		if verdict.Action != "warn_allow" {
			// Defensive: Check does not currently return "block"; if a future
			// detector does, surface it as a distinct (non-approval) reason class.
			if verdict.Action == "block" {
				return false, "blocked"
			}
			// ask_parent: first honour a still-valid parent approval for this
			// exact host, so a site a parent already OK'd is not re-blocked.
			// ctx carries the request lifecycle: a cancelled request aborts
			// the lookup/upsert before they complete.
			if a.db != nil {
				approvalID := proposalApprovalID(a.user.Name, "suspicious_site", host)
				prev, err := a.db.GetApproval(ctx, approvalID)
				if err != nil {
					// Fail closed on lookup errors: surface a fresh pending row so
					// the parent can decide, and log the fault.
					log.Printf("[agent] host-decision: approval lookup for %q failed: %v", approvalID, err)
					approval := &store.Approval{
						ID: approvalID, UserName: a.user.Name, UserDisplay: a.user.DisplayName,
						AgeGroup: a.user.AgeGroup, Category: "suspicious_site", QueryText: host,
					}
					if isNew, uerr := a.db.UpsertApproval(ctx, approval); uerr != nil {
						log.Printf("[agent] failed to create suspicious-site approval: %v", uerr)
					} else if isNew && a.onNewSuspiciousApproval != nil {
						if nerr := a.onNewSuspiciousApproval(ctx, approval); nerr != nil {
							log.Printf("[agent] suspicious-site approval notification failed for %q: %v", approval.ID, nerr)
						}
					}
				} else if prev != nil && prev.Status == "approved" && time.Now().Before(prev.ExpiresAt) {
					// Still valid: allow without touching the row.
					return true, ""
				} else if prev != nil && prev.Status == "approved" {
					// Granted earlier but now EXPIRED: reset to a fresh pending window
					// so the parent re-sees it on the dashboard instead of the host
					// staying silently blocked. (UpsertApproval is a no-op on an
					// existing row, so it cannot re-surface it.)
					if _, uerr := a.db.RequeueApproval(ctx, approvalID); uerr != nil {
						log.Printf("[agent] failed to requeue expired suspicious-site approval %q: %v", approvalID, uerr)
					}
				} else {
					// No prior approval (first request) — or a prior DENIAL, which we
					// leave blocked without re-nagging the parent.
					approval := &store.Approval{
						ID: approvalID, UserName: a.user.Name, UserDisplay: a.user.DisplayName,
						AgeGroup: a.user.AgeGroup, Category: "suspicious_site", QueryText: host,
					}
					if isNew, uerr := a.db.UpsertApproval(ctx, approval); uerr != nil {
						log.Printf("[agent] failed to create suspicious-site approval: %v", uerr)
					} else if isNew && a.onNewSuspiciousApproval != nil {
						if nerr := a.onNewSuspiciousApproval(ctx, approval); nerr != nil {
							log.Printf("[agent] suspicious-site approval notification failed for %q: %v", approval.ID, nerr)
						}
					}
				}
			}
			return false, "suspicious"
		}

		return true, ""
	}
}

// hostGateError builds the typed, sanitized error for a rejected host, using
// the reason class to pick the butler-facing wording without ever leaking the
// full allow/deny list (design §1.2, §3.3). It returns a
// *webfetch.HostNotAllowedError so the errors.As classification that separates
// configuration blocks from network failures keeps working across web_fetch,
// web_search, and the browser paths.
func (a *Agent) hostGateError(host, reason string) error {
	switch reason {
	case "denylist", "baseline", "suspicious":
		// "suspicious" is the distinct kid-safe parent-approval reason class
		// (a parent approval row is created in the approvals UI); the others
		// are config blocks. All map to the host-denied typed error.
		return webfetch.NewHostDeniedError(host, reason)
	default:
		return webfetch.NewHostNotAllowedError(host)
	}
}

// hostValidator is the host-gate callback passed to webfetch.Fetch, the
// search provider, and the browser pool: typed error on rejection, nil when
// the host passes. All redirect re-checks therefore use one decision.
func (a *Agent) hostValidator(ctx context.Context, host string) error {
	if ok, reason := a.hostDecision(ctx, host); !ok {
		return a.hostGateError(host, reason)
	}
	return nil
}

// handleWebFetch dispatches the builtin__web_fetch tool. Auth boundary:
// validates scheme, enforces the mode-aware host gate (allowlist or denylist,
// see hostDecision), and propagates the same gate into webfetch.Fetch as a
// HostValidator so it is re-applied to every redirect target.
// webfetch.Fetch additionally vetoes private/loopback/link-local IPs at the
// dialer when block_private_networks is set.
func (a *Agent) handleWebFetch(ctx context.Context, args map[string]any) (string, error) {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		return "", fmt.Errorf("web_fetch requires a 'url' argument")
	}

	cfg := a.cfg.Tools.WebFetch
	if !cfg.Enabled {
		return "", fmt.Errorf("web_fetch is disabled in this deployment")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("web_fetch only supports http(s) URLs, got %q", u.Scheme)
	}

	// Mode-aware host gate (design §1.2): hostDecision is the single source
	// of truth shared by web_fetch, web_search, the browser fallback, and
	// browser_navigate. In allowlist mode a non-empty allowlist gates fetches
	// exactly as before; with an empty/absent allowlist the config resolves to
	// denylist mode, so public hosts pass and only url_denylist hosts are
	// blocked — the "empty allowlist denies everything" footgun is gone.
	// The gate runs exactly once, inside webfetch.Fetch, via the HostValidator
	// option set below; a redundant early check here would double the
	// hostDecision GetApproval+upsert per successful fetch.
	opts := webfetch.Options{
		MaxBytes:      cfg.MaxBytes,
		Timeout:       time.Duration(cfg.TimeoutSec) * time.Second,
		HostValidator: a.hostValidator,
		// famclaw is a home-LAN product — by default we allow private/loopback
		// addresses so the bot can reach co-located services (SearXNG,
		// Playwright, llama-server, etc.). Admins opt INTO enterprise-style
		// SSRF blocking by setting tools.web_fetch.block_private_networks: true.
		AllowPrivateNetworks: !cfg.BlockPrivateNetworks,
	}
	// Caller-supplied max_bytes can only further restrict the cap, never
	// raise it. Reject non-integer / negative / overflow values up front
	// so a float like 0.4 doesn't silently truncate to int64(0) and reset
	// the cap to its default.
	if v, ok := args["max_bytes"].(float64); ok {
		if v < 1 || math.Trunc(v) != v || v > math.MaxInt64 {
			return "", fmt.Errorf("web_fetch max_bytes must be a positive integer")
		}
		caller := int64(v)
		if caller < opts.MaxBytes {
			opts.MaxBytes = caller
		}
	}

	result, err := a.webFetcher(ctx, rawURL, opts)
	// browserTried records that the browser fallback already ran for this
	// call. On the 403/429 path it stops the thin-fetch fallback below from
	// re-navigating a page we just rendered — a browser render is
	// authoritative; a second render would add latency, not text.
	browserTried := false
	if err != nil {
		log.Printf("[agent][%s] web_fetch url=%q err=%v", a.user.Name, rawURL, err)
		// F7 fix (web-retrieval plan, slice 3b): a 403/429 is usually
		// upstream bot-blocking of this client, not a dead page — the same
		// URL often renders fine in a real browser. When the browser
		// fallback is enabled and a pool exists, retry through the EXISTING
		// host gate before surfacing the HTTP error. fetchWithBrowser
		// re-validates the host (builtin__browser_navigate re-checks the
		// final URL after redirects), so no new gate is introduced.
		// 404/5xx, connection, and allowlist failures keep their original
		// error: the fallback exists for bot-blocked content only.
		if result != nil && isBotBlockStatus(result.StatusCode) &&
			cfg.FallbackToBrowser && a.browserPool != nil {
			directStatus := result.StatusCode
			log.Printf("[agent][%s] web_fetch: direct fetch got HTTP %d for %s; trying browser fallback",
				a.user.Name, directStatus, rawURL)
			browserResult, fbErr := a.fetchWithBrowser(ctx, a.browserPool, rawURL, a.hostValidator)
			if fbErr != nil {
				log.Printf("[agent][%s] web_fetch: browser fallback after HTTP %d failed for %s; returning original error: %v",
					a.user.Name, directStatus, rawURL, fbErr)
			} else {
				result = browserResult
				err = nil
				browserTried = true
				log.Printf("[agent][%s] web_fetch url=%q status=%d bytes=%d truncated=%v (via browser fallback after HTTP %d)",
					a.user.Name, rawURL, result.StatusCode, result.Bytes, result.Truncated, directStatus)
			}
		}
		if err != nil {
			return "", fmt.Errorf("web_fetch failed for %s: %v", rawURL, err)
		}
	}
	if result != nil {
		log.Printf("[agent][%s] web_fetch url=%q status=%d bytes=%d truncated=%v",
			a.user.Name, rawURL, result.StatusCode, result.Bytes, result.Truncated)
		text := strings.TrimSpace(result.Text)
		minLength := a.cfg.Tools.WebFetch.FallbackMinTextLength
		if minLength == 0 {
			minLength = 10
		}
		if len(text) < minLength && a.cfg.Tools.WebFetch.FallbackToBrowser && !browserTried {
			if a.browserPool != nil {
				// Attempt browser fallback for JS-heavy sites. fetchWithBrowser
				// guarantees a non-empty result on success — every failure mode
				// (browser error, empty render) returns a distinct error rather
				// than an empty success, so there is no separate empty-render
				// check for the fallback here. A nil/unavailable pool is handled
				// by the else-branch below, not passed into fetchWithBrowser.
				browserResult, err := a.fetchWithBrowser(ctx, a.browserPool, rawURL, a.hostValidator)
				if err != nil {
					log.Printf("[agent][%s] web_fetch browser fallback failed for %s, using original result: %v", a.user.Name, rawURL, err)
				} else {
					result = &webfetch.Result{
						URL:         browserResult.URL,
						StatusCode:  browserResult.StatusCode,
						ContentType: browserResult.ContentType,
						Bytes:       browserResult.Bytes,
						Truncated:   browserResult.Truncated,
						Text:        browserResult.Text,
					}
					log.Printf("[agent][%s] web_fetch url=%q status=%d bytes=%d truncated=%v (via browser fallback)",
						a.user.Name, rawURL, result.StatusCode, result.Bytes, result.Truncated)
				}
			} else {
				// Browser fallback is enabled but no browser pool is available
				// to service it (no tools.browser.endpoint configured, or the pool
				// failed to initialize). This is a degraded state, not a crash:
				// return the thin HTTP text as-is, but log the missed fallback so
				// the operator can see the degradation rather than silent thin text.
				log.Printf("[agent][%s] web_fetch: browser fallback is enabled but no browser pool is configured; cannot fall back for %s (plain fetch returned %d chars, threshold %d)",
					a.user.Name, rawURL, len(text), minLength)
			}
		}
		if strings.TrimSpace(result.Text) == "" {
			return "", fmt.Errorf("web_fetch got an empty response from %s (HTTP %d); the page returned no readable text", rawURL, result.StatusCode)
		}
	}

	// Spillover: when the cache is configured, payloads larger than the
	// current head budget get stored and only a budget-sized slice plus
	// a tool_result_more invocation hint is returned to the LLM. This is
	// the v0.5.9 overflow class fix — before this, the full ~256KB
	// payload went straight into context and 400'd Nemotron at 16k ctx.
	if a.cache != nil {
		budget := computeHeadBudget(a)
		out, cerr := a.cache.Put(ctx, toolcache.PutInput{
			User: a.user.Name, UserRole: a.user.Role, ConvID: a.convID,
			ToolName: "builtin__web_fetch",
			Args: map[string]any{
				"url":       rawURL,
				"max_bytes": opts.MaxBytes,
			},
			Payload:     []byte(result.Text),
			ContentType: result.ContentType,
			Category:    "web_fetch",
			HeadBudget:  budget,
		})
		if cerr == nil {
			header := fmt.Sprintf("URL: %s\nStatus: %d\nContent-Type: %s\nBytes total: %d\nCache id: %s\n\n",
				result.URL, result.StatusCode, result.ContentType, out.TotalBytes, out.ID)
			if out.Truncated {
				footer := fmt.Sprintf("\n\n[truncated — %d chars remaining. To read more, call:\n  tool_result_more(id=%q, offset=%d, length=8000)\n]",
					out.TotalBytes-len(out.Head), out.ID, len(out.Head))
				return header + string(out.Head) + footer, nil
			}
			return header + string(out.Head), nil
		}
		// Cache write failed — log and fall through to legacy inline path
		// so the user still gets a (possibly oversize) response rather
		// than no response at all.
		log.Printf("[agent][%s] cache spillover failed, returning full payload inline: %v",
			a.user.Name, cerr)
	}

	// Legacy path: no cache configured, or cache write failed.
	return fmt.Sprintf("URL: %s\nStatus: %d\nContent-Type: %s\nTruncated: %v\n\n%s",
		result.URL, result.StatusCode, result.ContentType, result.Truncated, result.Text), nil
}

// BrowserFetcher is the subset of *browser.Pool the agent uses: the
// builtin__browser_* tool dispatch (Exec) and the web_fetch browser fallback
// (NavigateAndExtract). *browser.Pool satisfies it; tests substitute a fake
// to exercise the fallback without a live Playwright server.
type BrowserFetcher interface {
	// Exec dispatches a builtin__browser_* tool through the pool.
	Exec(ctx context.Context, in browser.ExecInput) (string, error)

	// NavigateAndExtract navigates the pool's page to rawURL (http/https
	// only, the host gate before and after redirects) and returns the
	// rendered body text plus the size of the navigated document's HTTP
	// response body in transferred (wire) bytes — the Content-Length header
	// or the sum of chunks for a chunked response; 0 when it could not be
	// determined.
	NavigateAndExtract(ctx context.Context, user, rawURL string, hostCheck browser.HostValidator) (string, int64, error)
}

// isBotBlockStatus reports whether an HTTP status is the kind of failure the
// browser fallback should absorb on a web_fetch error: 403 (bot-blocked) and
// 429 (rate-limited). 404, 5xx, and transport-level failures are not
// bot-blocking — the fallback must not fire for them, or a genuinely missing
// page would be "fixed" by re-rendering it in a browser.
func isBotBlockStatus(status int) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests
}

// fetchWithBrowser runs the browser fallback for JS-heavy sites: it
// navigates the browser pool to rawURL and extracts rendered text. The
// hostCheck validator (the shared hostValidator) is applied to navigation —
// the pool re-checks the final URL after redirects with the same callback.
//
// Lifecycle is acquire-use-release through the pool: NavigateAndExtract
// acquires/reuses a per-user page, reads it, and the pool's idle-sweeper
// releases the session when idle — no new browser is spawned per fetch.
//
// Bytes reports the page's actual size in transferred (wire) bytes — the
// HTTP response's Content-Length header, or the sum of chunks for a chunked
// transfer-encoding response — as reported by the pool; it is deliberately
// not the length of the extracted text (issue #246 review decision).
//
// Every failure path returns a distinct, honest error — the caller never
// receives an empty "success":
//   - nil/unavailable pool: "browser fetch: browser pool is not configured"
//   - navigate/extract failure: "browser fetch: <stage> <url>: <err>"
//   - empty rendered result: "browser fetch: rendered content empty for <url>"
func (a *Agent) fetchWithBrowser(ctx context.Context, pool BrowserFetcher, rawURL string, hostCheck browser.HostValidator) (*webfetch.Result, error) {
	if pool == nil {
		return nil, fmt.Errorf("browser fetch: browser pool is not configured (set tools.browser.endpoint; fallback_to_browser requires a browser pool)")
	}
	// One pool call does the navigate (host gate before and after redirects)
	// and the rendered-text extract, and reports the page's actual wire size
	// rather than the extracted-text length.
	out, wireBytes, err := pool.NavigateAndExtract(ctx, a.user.Name, rawURL, hostCheck)
	if err != nil {
		return nil, fmt.Errorf("browser fetch: %w", err)
	}
	// The browser extract tool returns plain text; status/content-type are not
	// available from the browser path, so use sensible defaults. An empty
	// render is an honest failure — never a silent empty success.
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, fmt.Errorf("browser fetch: rendered content empty for %s", rawURL)
	}
	return &webfetch.Result{
		URL:         rawURL,
		StatusCode:  200,
		ContentType: "text/plain",
		Bytes:       wireBytes,
		Truncated:   false,
		Text:        text,
	}, nil
}

// handleWebSearch dispatches the builtin__web_search tool. Auth boundary:
// reuses the same mode-aware host gate as web_fetch (hostDecision) plus the
// same private-net policy. In allowlist mode the search endpoint host must be
// allowlisted. In denylist mode the operator-pinned endpoint host still goes
// through the full hostDecision: the bundled baseline + parent denylist apply,
// and a public-IP-literal endpoint (e.g. a VPS-hosted SearXNG) is routed to a
// one-time parent approval (ask_parent) before the first search succeeds — a
// fail-safe for a home-LAN product where SearXNG normally runs locally
// (design §1.5). The endpoint is operator-supplied (the LLM never supplies it),
// so this gate is a config sanity check, not a user-facing host restriction.
// The model gets a compact title/url/snippet listing instead of the raw
// 4-16KB SearXNG JSON.
func (a *Agent) handleWebSearch(ctx context.Context, args map[string]any) (string, error) {
	cfg := a.cfg.Tools.WebSearch
	if !cfg.Enabled {
		return "", fmt.Errorf("web_search is disabled; enable it via tools.web_search.enabled=true, with tools.web_fetch.enabled=true (the search endpoint host is gated by the web_fetch URL policy)")
	}
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("web_search requires a 'query' argument")
	}

	maxResults := readIntArg(args, "max_results", cfg.MaxResults)

	fcfg := a.cfg.Tools.WebFetch

	// Provider boundary (web-retrieval plan, Slice 1): the backend is
	// selected from config; SearXNG is the default and keeps its current
	// behavior byte-for-byte (same endpoint, same host gate, same private-net
	// policy). The hosted Tavily path uses its fixed vendor host — NOT the
	// user-fetch host gate (a different surface; see websearch.TavilyProvider).
	prov, provErr := websearch.ProviderFor(websearch.ProviderConfig{
		Name:       cfg.Provider,
		Endpoint:   cfg.Endpoint,
		MaxResults: maxResults,
		Timeout:    time.Duration(cfg.TimeoutSec) * time.Second,
		// The mode-aware host gate, shared with web_fetch (design §1.2, §1.5).
		HostValidator:        a.hostValidator,
		AllowPrivateNetworks: !fcfg.BlockPrivateNetworks,
		TavilyAPIKey:         cfg.TavilyAPIKey,
	})
	if provErr != nil {
		return webSearchError(provErr, cfg.Endpoint)
	}

	hits, err := prov.Search(ctx, query, maxResults)
	if err != nil {
		return webSearchError(err, cfg.Endpoint)
	}
	log.Printf("[agent][%s] web_search provider=%s query=%q hits=%d", a.user.Name, prov.Name(), query, len(hits))
	return websearch.FormatHits(hits), nil
}

// webSearchError translates a web_search error into a (result, error) pair
// that the agent's tool loop can feed to the LLM. Each backend failure class
// maps to a short NEUTRAL marker returned as the RESULT string with a nil
// error — this is critical because the tool loop formats non-nil errors as
// "Error: <msg>", which the LLM may treat as a system failure and ignore in
// favour of hallucination. A result string with nil error is treated as a
// normal tool output, so the LLM relays the marker honestly. Class-specific
// markers (rate-limited vs down vs misconfigured) let the model report the
// real reason instead of a blanket "unavailable."
//
// The markers are intentionally NOT full English sentences addressed to the
// user. The behavioural rule in internal/prompt/components.go instructs the
// model to convey this in the family member's language. Returning a
// pre-written sentence would force it into English regardless of who is
// asking.
//
// The sanitized endpoint is logged server-side for debugging but never
// echoed to the family chat — it may contain internal hostnames,
// credentials, or network topology that should not reach the user.
//
// Host-not-allowed errors and configuration gaps (unknown provider, hosted
// provider without a key) are translated into sanitized result strings so
// the tool loop does not format them as "Error: <msg>". Both pre-request
// checks key on typed sentinels, never message text, so an upstream rework
// of the wording cannot change the classification: errors.As on
// *webfetch.HostNotAllowedError, and errors.Is on
// websearch.ErrProviderNotConfigured. Other genuine errors pass through as
// ("", err) for the tool loop to surface.
func webSearchError(err error, endpoint string) (string, error) {
	if err == nil {
		return "", nil
	}
	var hostErr *webfetch.HostNotAllowedError
	if errors.As(err, &hostErr) {
		return "web_search: host not permitted", nil
	}
	// Provider-boundary configuration gaps (web-retrieval plan, Slice 1):
	// unknown provider name or hosted provider without a key. Classified by
	// the sentinel, so upstream rewording cannot break the mapping.
	if errors.Is(err, websearch.ErrProviderNotConfigured) {
		return "web_search: provider not configured", nil
	}
	// Classified backend failures (web-retrieval plan, Slice 2): check the
	// specific classes BEFORE the ErrUnavailable umbrella they all wrap —
	// the residual (e.g. 400/404 or a plain wrapped legacy error) keeps
	// the historical "unavailable" marker.
	switch {
	case errors.Is(err, websearch.ErrRateLimited):
		log.Printf("[agent] web_search rate-limited (429) at %s", websearch.SanitizeEndpoint(endpoint))
		return "web_search: rate limited", nil
	case errors.Is(err, websearch.ErrProvider5xx):
		log.Printf("[agent] web_search backend server error (5xx) at %s: %v", websearch.SanitizeEndpoint(endpoint), err)
		return "web_search: search backend error", nil
	case errors.Is(err, websearch.ErrTimeout):
		log.Printf("[agent] web_search request timed out at %s", websearch.SanitizeEndpoint(endpoint))
		return "web_search: search timed out", nil
	case errors.Is(err, websearch.ErrConnection):
		log.Printf("[agent] web_search connection failure at %s: %v", websearch.SanitizeEndpoint(endpoint), err)
		return "web_search: search backend unreachable", nil
	case errors.Is(err, websearch.ErrAuthConfig):
		log.Printf("[agent] web_search backend rejected the request (401/403) at %s: %v", websearch.SanitizeEndpoint(endpoint), err)
		return "web_search: search backend auth or config error", nil
	case errors.Is(err, websearch.ErrMalformed):
		log.Printf("[agent] web_search malformed backend response at %s: %v", websearch.SanitizeEndpoint(endpoint), err)
		return "web_search: invalid search backend response", nil
	case errors.Is(err, websearch.ErrUnavailable):
		log.Printf("[agent] web_search backend unreachable at %s: %v", websearch.SanitizeEndpoint(endpoint), err)
		return "web_search: unavailable", nil
	}
	return "", err
}

// handleBrowser dispatches all builtin__browser_* tools. Auth boundary:
// reuses the mode-aware web_fetch host gate (hostDecision) as the navigation
// host gate (only browser_navigate consults it; subsequent click/fill/extract
// act on the current page so they inherit the validated host).
func (a *Agent) handleBrowser(ctx context.Context, toolName string, args map[string]any) (string, error) {
	if a.browserPool == nil {
		return "", fmt.Errorf("%s: browser tools are disabled in this deployment", toolName)
	}
	out, err := a.browserPool.Exec(ctx, browser.ExecInput{
		User:     a.user.Name,
		ToolName: toolName,
		Args:     args,
		// The pool re-checks the final URL after redirects with this same
		// mode-aware gate (design §1.2, §1.4).
		HostCheck: a.hostValidator,
	})
	if err != nil {
		log.Printf("[agent][%s] %s err=%v", a.user.Name, toolName, err)
		return "", err
	}
	log.Printf("[agent][%s] %s ok bytes=%d", a.user.Name, toolName, len(out))
	return out, nil
}

// handleToolResultMore dispatches the builtin__tool_result_more tool.
// Returns rendered content (or an explanatory error string suitable for the
// LLM transcript) for the LLM. Per-user ownership is enforced inside
// Cache.More — cross-user access surfaces as ErrNotFound here.
func (a *Agent) handleToolResultMore(ctx context.Context, args map[string]any) (string, error) {
	if a.cache == nil {
		return "", fmt.Errorf("tool_result_more: cache not configured in this deployment")
	}
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("tool_result_more requires 'id'")
	}
	offset := readIntArg(args, "offset", 0)
	length := readIntArg(args, "length", 4096)
	if offset < 0 {
		return "", fmt.Errorf("tool_result_more: offset must be >= 0, got %d", offset)
	}
	if length <= 0 {
		return "", fmt.Errorf("tool_result_more: length must be > 0, got %d", length)
	}

	out, err := a.cache.More(ctx, a.user.Name, id, offset, length)
	if err != nil {
		if errors.Is(err, toolcache.ErrNotFound) {
			return "[cache id not found or expired]", nil
		}
		return "", err
	}
	// Only stringify text-type payloads to chat. Binary types (image/*,
	// audio/*) return a marker — vision/voice handling lands in Phase 5.
	if !strings.HasPrefix(out.ContentType, "text/") {
		return fmt.Sprintf("[%s; %d bytes at offset %d; binary payload not renderable to chat yet]",
			out.ContentType, out.Length, out.Offset), nil
	}
	body := string(out.Data)
	if out.Offset+out.Length < out.TotalBytes {
		body += fmt.Sprintf("\n\n[%d bytes remaining; call tool_result_more(id=%q, offset=%d) to read more]",
			out.TotalBytes-(out.Offset+out.Length), id, out.Offset+out.Length)
	}
	return body, nil
}

// handleProposeFamilyFact accepts a fact proposal from any role.
//
// PARENT path: an OPA tool-call check against the synthetic name
// "family_fact_proposal_auto_apply" decides whether to apply directly.
// This is intentional defense in depth — without it a Go bug could let a
// non-parent slip through this branch (R3 council "OPA hole" closure).
//
// CHILD path: the proposal is JSON-encoded and written into approvals.
// query_text with Category=ProposalKind. A parent then calls
// approve_request, which dispatches on Category and applies the fact.
func (a *Agent) handleProposeFamilyFact(ctx context.Context, args map[string]any) (string, error) {
	if a.familyState == nil {
		return "", fmt.Errorf("propose_family_fact: family state is not configured")
	}
	category, _ := args["category"].(string)
	subject, _ := args["subject"].(string)
	label, _ := args["label"].(string)
	value, _ := args["value"].(string)
	reason, _ := args["reason"].(string)

	if category == "" || subject == "" || label == "" || value == "" {
		return "", fmt.Errorf("propose_family_fact: category, subject, label, and value are all required")
	}
	if len(label) > 64 {
		return "label too long (max 64 chars)", nil
	}
	if len(value) > 512 {
		return "value too long (max 512 chars)", nil
	}
	if !knownSubjects(a.cfg)[subject] {
		return fmt.Sprintf("subject %q is not a family member", subject), nil
	}

	if a.user.Role == "parent" {
		if a.evaluator != nil {
			dec, err := a.evaluator.EvaluateToolCall(ctx, policy.ToolCallInput{
				User:     policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup, Name: a.user.Name},
				ToolName: "family_fact_proposal_auto_apply",
			})
			if err != nil {
				return "", fmt.Errorf("propose_family_fact opa: %w", err)
			}
			if !dec.Allow {
				return "Not authorized to auto-apply this proposal.", nil
			}
		}
		f := familystate.Fact{
			Category: category, Subject: subject, Label: label, Value: value,
			CreatedBy: a.user.Name,
		}
		if err := a.familyState.UpsertFact(ctx, &f); err != nil {
			if errors.Is(err, familystate.ErrUnknownCategory) {
				return fmt.Sprintf("unknown category %q — create it with add_family_category first", category), nil
			}
			return "", fmt.Errorf("propose_family_fact upsert: %w", err)
		}
		auditArgs, _ := json.Marshal(map[string]any{
			"category": category, "subject": subject, "label": label, "value": value,
			"id": f.ID, "auto_apply_parent": true,
		})
		if a.db != nil {
			_ = a.db.LogAudit(ctx, a.user.Name, a.auditGateway, "builtin__propose_family_fact", auditArgs)
		}
		return fmt.Sprintf("ok — fact #%d applied directly (parent auto-apply)", f.ID), nil
	}

	envelope, err := familystate.EncodeProposal(familystate.Proposal{
		Category: category, Subject: subject, Label: label, Value: value,
		Reason: reason, ProposedBy: a.user.Name,
	})
	if err != nil {
		return "", fmt.Errorf("propose_family_fact encode: %w", err)
	}
	approval := &store.Approval{
		ID:          proposalApprovalID(a.user.Name, subject, label),
		UserName:    a.user.Name,
		UserDisplay: a.user.DisplayName,
		AgeGroup:    a.user.AgeGroup,
		Category:    familystate.ProposalKind,
		QueryText:   string(envelope),
	}
	if a.db == nil {
		return "", fmt.Errorf("propose_family_fact: db not configured")
	}
	if _, err := a.db.UpsertApproval(ctx, approval); err != nil {
		return "", fmt.Errorf("propose_family_fact approval: %w", err)
	}
	return "Proposal sent to parents.", nil
}

// proposalApprovalID makes a stable, per-proposal id derived from the
// proposing user, subject, label, and the current day. Re-proposing the
// exact same triple within a day deduplicates onto the existing pending
// row (UpsertApproval is idempotent on id collision).
func proposalApprovalID(userName, subject, label string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte("family_fact_proposal:" + userName + ":" + subject + ":" + label + ":" + day))
	return hex.EncodeToString(h[:8])
}

// handleGetFamilyState dispatches builtin__get_family_state. Reads family_facts
// (optionally filtered to one category) and returns a rendered text block
// grouped by category. Open to all roles — no admin gate; OPA tool_policy
// already permits the bare name. The render format mirrors Snapshot.Render's
// style but is NOT wrapped in <family_safety> tags — that wrapper is reserved
// for the always-injected path so the model can distinguish "safety context"
// from "tool result".
func (a *Agent) handleGetFamilyState(ctx context.Context, args map[string]any) (string, error) {
	if a.familyState == nil {
		return "Family state is not configured.", nil
	}
	category, _ := args["category"].(string)

	facts, err := a.familyState.ListFacts(ctx, familystate.FilterOpts{Category: category})
	if err != nil {
		return "", fmt.Errorf("get_family_state: %w", err)
	}
	if len(facts) == 0 {
		if category != "" {
			return fmt.Sprintf("No facts in category %q.", category), nil
		}
		return "No family facts have been recorded yet.", nil
	}

	known := knownSubjects(a.cfg)
	byCat := map[string][]familystate.Fact{}
	for _, f := range facts {
		if !known[f.Subject] {
			// Skip orphans — same rule as the snapshot reader. The
			// dashboard surfaces them separately for parent cleanup.
			continue
		}
		byCat[f.Category] = append(byCat[f.Category], f)
	}
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var b strings.Builder
	for _, c := range cats {
		fmt.Fprintf(&b, "%s:\n", c)
		for _, f := range byCat[c] {
			fmt.Fprintf(&b, "  - %s — %s: %s\n", f.Subject, f.Label, f.Value)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// readIntArg pulls an int from args, defaulting on missing / non-numeric.
// JSON-decoded numbers arrive as float64; we accept that and plain int.
func readIntArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	}
	return def
}

// normalizeTimeoutSeconds extracts timeout_seconds from spawn_agent args.
// JSON numbers decode to float64 in Go, so we read it as a float and treat
// missing, non-numeric, zero, negative, or sub-second values as "use the
// default." This avoids fractional inputs (e.g. 0.5) silently truncating to
// an int 0 and producing an immediate-deadline timeout.
func normalizeTimeoutSeconds(args map[string]any) int {
	ts, ok := args["timeout_seconds"].(float64)
	if !ok || ts < 1 {
		return subagentDefaultTimeoutSec
	}
	if ts > float64(subagentMaxTimeoutSec) {
		return subagentMaxTimeoutSec
	}
	return int(ts)
}

// parseStringList converts a JSON-decoded []any into []string. Non-string
// elements are skipped. Returns nil for nil/empty input or non-slice values.
func parseStringList(v any) []string {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildPromptSnapshots builds the always-inject family-state snapshot and the
// per-user memory snapshot for the current user. It is the single construction
// point shared by both prompt paths (operator-override and structured builder)
// so the two never diverge on which stores are consulted.
//
// A nil store yields a nil snapshot — a no-op at render time. Snapshot errors
// are logged and downgraded to nil so a database hiccup never breaks the turn.
func (a *Agent) buildPromptSnapshots(ctx context.Context) (*familystate.Snapshot, *usermemory.Snapshot) {
	var fsSnap *familystate.Snapshot
	if a.familyState != nil {
		snap, err := a.familyState.AlwaysInjectedSnapshot(ctx, knownSubjects(a.cfg))
		if err != nil {
			log.Printf("[agent][%s] family-state snapshot error (omitting safety block): %v", a.user.Name, err)
		} else {
			fsSnap = snap
		}
	}

	var umSnap *usermemory.Snapshot
	if a.userMemory != nil {
		snap, err := a.userMemory.AlwaysInjectedSnapshot(ctx, a.user.Name)
		if err != nil {
			log.Printf("[agent][%s] user-memory snapshot error (omitting memory block): %v", a.user.Name, err)
		} else {
			umSnap = snap
		}
	}

	return fsSnap, umSnap
}

func (a *Agent) buildMessages(ctx context.Context, history []*store.Message, currentMessage string) []llm.Message {
	var msgs []llm.Message

	// Build always-inject family-state and per-user memory snapshots once,
	// shared by both prompt paths. See buildPromptSnapshots for nil/error handling.
	fsSnap, umSnap := a.buildPromptSnapshots(ctx)

	var systemPrompt string
	if a.cfg.LLM.SystemPrompt != "" {
		// Operator override — start from the custom prompt, then always
		// append the age-appropriate context and the capabilities section so
		// the model knows about its tools (web_search/web_fetch/spawn_agent)
		// even under a custom system_prompt.
		systemPrompt = a.cfg.LLM.SystemPrompt + "\n\n" + ageContextPrompt(a.user)

		// Compute the builtin tools the model should be told about.
		var builtinNames []string
		for _, t := range a.builtinTools {
			if t.AllowedForRole(a.user.Role) {
				builtinNames = append(builtinNames, strings.TrimPrefix(t.Name, "builtin__"))
			}
		}
		// When builtin tools are present, capabilitiesComponent bundles the
		// behavioral guardrails (tool-call format + grounding); when there
		// are none, fall back to the standalone rules to preserve leak/
		// hallucination protection.
		if len(builtinNames) > 0 {
			systemPrompt += "\n\n" + prompt.Capabilities(prompt.BuildContext{BuiltinTools: builtinNames})
		} else {
			systemPrompt += "\n\n" + prompt.BehavioralRules()
		}
		if len(a.skills) > 0 {
			// When evaluator is available, use the policy-checked version
			if a.evaluator != nil {
				sp, err := skillbridge.LoadForPromptChecked(ctx, a.skills,
					&skillPromptAdapter{eval: a.evaluator}, a.user.Role)
				if err != nil {
					// Treat evaluator failure as empty skills (fail open on loader error to avoid
					// blocking the agent entirely; the output gate will still catch bad content)
					sp = ""
				}
				if sp != "" {
					systemPrompt += "\n\n" + sp
				}
			} else {
				if sp := skillbridge.LoadForPrompt(a.skills); sp != "" {
					systemPrompt += "\n\n" + sp
				}
			}
		}
		// Append the always-injected family-state and per-user memory blocks.
		// Render() returns "" for nil/empty snapshots, so these are no-ops when
		// there is nothing to inject.
		if rendered := fsSnap.Render(); rendered != "" {
			systemPrompt += "\n\n" + rendered
		}
		if rendered := umSnap.Render(); rendered != "" {
			systemPrompt += "\n\n" + rendered
		}
	} else {
		// Default — use the structured PromptBuilder.
		skillNames := make([]string, 0, len(a.skills))
		for _, sk := range a.skills {
			if sk != nil {
				skillNames = append(skillNames, sk.Name)
			}
		}
		// Filter builtins by BOTH the role gate AND the OPA tool_policy
		// gate, so prompt hints don't advertise tools the user will be
		// blocked from calling at the tool loop. (e.g. an operator who
		// puts age_8_12 in allowed_roles still has tool_policy denying
		// web_fetch for that age.) The evaluator is in-memory; using a
		// background context here is fine.
		//
		// However, to prevent over-deflection (where model thinks it can't
		// use a tool even though it's allowed by role), we need to ensure
		// that tools that are allowed by role are always advertised in the
		// capabilities prompt, regardless of policy filtering for this specific
		// user. This preserves the role-based allowance as a hint to the model.
		var builtinNames []string
		roleAllowedNames := make(map[string]bool) // Track tools allowed by role
		builtinNameSet := make(map[string]bool)   // Track what's already in builtinNames for O(1) lookup
		for _, t := range a.builtinTools {
			bare := strings.TrimPrefix(t.Name, "builtin__")

			// Check role allowance first
			if t.AllowedForRole(a.user.Role) {
				roleAllowedNames[bare] = true
			}

			// Then apply policy filtering for prompt inclusion
			if !t.AllowedForRole(a.user.Role) {
				continue
			}
			if a.evaluator != nil {
				dec, err := a.evaluator.EvaluateToolCall(ctx, policy.ToolCallInput{
					User:     policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup, Name: a.user.Name},
					ToolName: bare,
				})
				if err != nil || !dec.Allow {
					continue
				}
			}
			builtinNames = append(builtinNames, bare)
			builtinNameSet[bare] = true
		}

		// Ensure that tools allowed by role are included in prompt even if policy
		// blocks them for this specific user. This prevents over-deflection.
		// We don't want to duplicate entries, so we check if already included using a set.
		for roleName, allowed := range roleAllowedNames {
			if allowed && !builtinNameSet[roleName] {
				builtinNames = append(builtinNames, roleName)
				builtinNameSet[roleName] = true
			}
		}

		systemPrompt = prompt.Build(prompt.BuildContext{
			Cfg:          a.cfg,
			User:         a.user,
			Skills:       skillNames,
			BuiltinTools: builtinNames,
			FamilyState:  fsSnap,
			UserMemory:   umSnap,
			// Gateway and HardBlocked left empty for now — wired by future PRs
			// that thread gateway and policy info through Agent.
		})
	}

	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})

	// Mark history messages as prunable so they are preferred for dropping
	// when compressing to fit the context window.
	prunableIdx := make(map[int]bool)
	historyIndex := 0
	for _, m := range history {
		if m.PolicyAction == "allow" || m.PolicyAction == "" {
			msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
			prunableIdx[len(msgs)-1] = true // index of the message we just added
			historyIndex++
		}
	}

	// Build the current user message. When the inbound message carries
	// attachments, build multimodal ContentParts (text + image_url data
	// URIs) so image content reaches a vision-capable model. The gateway
	// adapters already base64-encoded the image into gateway.Attachment.Data
	// with the correct MIMEType.
	//
	// NOTE on the replace-vs-append below: Chat() calls SaveMessage() for the
	// current user message BEFORE GetConversationHistory(), so the current
	// message is always the last entry in `history` (Role == "user"). That
	// means the history loop above already appended it as the last message in
	// `msgs`. When attachments are present we must replace that text-only
	// copy with the multimodal version rather than append a duplicate;
	// without attachments we leave it as-is (text-only, byte-identical).
	var userMsg llm.Message
	if len(a.msgContext.Attachments) > 0 {
		parts := []any{
			map[string]any{"type": "text", "text": currentMessage},
		}
		for _, att := range a.msgContext.Attachments {
			if att.Type == "image" && att.Data != "" {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + att.MIMEType + ";base64," + att.Data,
					},
				})
			}
		}
		userMsg = llm.Message{Role: "user", ContentParts: parts}
	} else {
		userMsg = llm.Message{Role: "user", Content: currentMessage}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" {
		msgs = append(msgs, userMsg)
	} else {
		// Current user message is already the last entry in msgs (from DB,
		// text-only). Replace it with the multimodal version so images are
		// included without duplicating the text.
		msgs[len(msgs)-1] = userMsg
	}

	// Compress the message list to fit within the configured context window,
	// leaving room for the response. Reuse the same logic as the tool loop.
	if a.cfg.LLM.MaxContextTokens > 0 {
		// Convert to compress messages
		var cmsgs []compress.Message
		for i, m := range msgs {
			cmsgs = append(cmsgs, compress.Message{
				Role:         m.Role,
				Content:      m.Content,
				ContentParts: m.ContentParts,
				Prunable:     prunableIdx[i],
			})
		}
		// Compress
		budget := a.cfg.LLM.MaxContextTokens - a.cfg.LLM.MaxResponseTokens
		if budget <= 0 {
			budget = a.cfg.LLM.MaxContextTokens
		}
		compressed := compress.Compress(cmsgs, compress.Options{ContextWindow: budget})
		// Convert back
		msgs = make([]llm.Message, 0, len(compressed))
		for _, cm := range compressed {
			msgs = append(msgs, llm.Message{
				Role:         cm.Role,
				Content:      cm.Content,
				ContentParts: cm.ContentParts,
			})
		}
	}

	return msgs
}
func defaultSystemPrompt(user *config.UserConfig) string {
	base := "You are FamClaw, a helpful, friendly, and safe family AI assistant."
	return base + "\n\n" + ageContextPrompt(user)
}

func ageContextPrompt(user *config.UserConfig) string {
	switch user.AgeGroup {
	case "under_8":
		return fmt.Sprintf("You are talking with %s, who is a young child (under 8). Use very simple words, short sentences, and be playful and encouraging. No scary or complex topics.", user.DisplayName)
	case "age_8_12":
		return fmt.Sprintf("You are talking with %s, who is between 8 and 12 years old. Be friendly and educational. Explain things clearly without being condescending.", user.DisplayName)
	case "age_13_17":
		return fmt.Sprintf("You are talking with %s, a teenager. Be respectful and treat them as a capable young adult. You can discuss more complex topics but remain age-appropriate.", user.DisplayName)
	default:
		return ""
	}
}

// skillPromptAdapter bridges *policy.Evaluator to skillbridge.SkillPromptEvaluator.
// It lives in internal/agent which imports both packages, avoiding a direct
// internal/skillbridge → internal/policy dependency.
type skillPromptAdapter struct {
	eval *policy.Evaluator
}

func (a *skillPromptAdapter) EvaluateSkillPrompt(ctx context.Context, in skillbridge.SkillPromptCheckInput) (skillbridge.SkillPromptCheckResult, error) {
	dec, err := a.eval.EvaluateSkillPrompt(ctx, policy.SkillPromptInput{
		SkillName:  in.SkillName,
		PromptBody: in.PromptBody,
		UserRole:   in.UserRole,
	})
	if err != nil {
		return skillbridge.SkillPromptCheckResult{}, err
	}
	return skillbridge.SkillPromptCheckResult{Allow: dec.Allow, Reason: dec.Reason}, nil
}

// ApprovalID generates a deterministic approval ID for user+category+day.
func ApprovalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}

// isSubagentExcludedTool reports whether the named builtin tool must NOT be
// exposed to subagents. Subagents inherit the parent's identity but stay
// read-only: they can read family state (get_family_state) and fetch URLs
// (web_fetch) but cannot mutate shared state, cannot spawn further agents,
// and cannot perform admin actions.
func isSubagentExcludedTool(name string) bool {
	switch name {
	case "builtin__spawn_agent",
		// Phase 3.3 family-state mutations:
		"builtin__set_family_fact",
		"builtin__delete_family_fact",
		"builtin__add_family_category",
		"builtin__delete_family_category",
		"builtin__propose_family_fact",
		// Admin tools:
		"builtin__approve_request",
		"builtin__deny_request",
		"builtin__set_user_role",
		"builtin__link_account",
		// URL allowlist management (parent-only):
		"builtin__allowlist_list",
		"builtin__allowlist_add",
		"builtin__allowlist_remove":
		return true
	}
	return false
}

// knownSubjects builds the set of valid subjects for family-state rows:
// every config.Users[].Name plus the literal "family". The familystate
// snapshot reader uses this set to skip orphan rows whose subject no
// longer matches a configured user (e.g. after a rename).
func knownSubjects(cfg *config.Config) map[string]bool {
	out := map[string]bool{"family": true}
	if cfg == nil {
		return out
	}
	for _, u := range cfg.Users {
		out[u.Name] = true
	}
	return out
}
