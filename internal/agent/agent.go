// Package agent implements the FamClaw conversation loop.
// Each user gets an isolated conversation with policy enforcement at every turn.
// Delegates to agentcore.Pipeline for the actual processing stages.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/browser"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/familystate"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/prompt"
	"github.com/famclaw/famclaw/internal/reminder"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/todo"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/usermemory"
	"github.com/famclaw/famclaw/internal/webfetch"
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
	gateway      string // gateway name (telegram, discord, web, etc.) for audit logs

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

	// cache is the tool-result spillover store. When non-nil, large
	// handleWebFetch (and other large-result) tool replies are written
	// to the cache and only a budget-sized head slice is returned to the
	// LLM. When nil, the legacy inline-everything path runs.
	cache *toolcache.Cache

// browserPool drives the builtin__browser_* tools when configured.
// nil = browser tools disabled.
	browserPool *browser.Pool

	// todoStore is the per-user todo store.
	todoStore *todo.Store

	// msgContext holds the gateway-specific context for the current message
	// (gateway, external_id, group_id, is_group). Used by outbound tools
	// like reminders to know where to send the notification.
	msgContext gateway.MsgContext
}

// AgentDeps holds optional dependencies for an Agent. All fields are
// safe to leave nil — the Agent degrades gracefully (no MCP tools,
// no skills, no scanning, no subagents).
type AgentDeps struct {
	Pool         *mcp.Pool
	Skills       []*skillbridge.Skill
	Quarantine   *skillbridge.Quarantine
	Scanner      skillbridge.Scanner
	Scheduler    *subagent.Scheduler
	BuiltinTools []agentcore.Tool
	Gateway      string           // gateway name (telegram, discord, web, etc.) for audit logs
	Cache        *toolcache.Cache // tool-result spillover cache; nil disables spillover (legacy inline path)
	BrowserPool  *browser.Pool    // backs builtin__browser_*; nil disables browser tools
	MsgContext   gateway.MsgContext // gateway-specific context for outbound tools (reminders, etc.)
}

// NewAgent creates an Agent for the given user. Optional dependencies
// (MCP pool, skills, scanner, scheduler, etc.) are passed in deps —
// any field left as zero value disables that capability for this Agent.
func NewAgent(user *config.UserConfig, cfg *config.Config, llmClient llm.Chatter,
	evaluator *policy.Evaluator, clf *classifier.Classifier, db *store.DB,
	deps AgentDeps) *Agent {

	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(user.Name + ":" + day))
	convID := hex.EncodeToString(h[:8])

	// Append admin tools for parent users so they are always available
	// without callers needing to wire them individually.
	builtins := deps.BuiltinTools
	if user.Role == "parent" {
		builtins = append(builtins, admin.AllDefinitions()...)
	}

	var fs *familystate.Store
		var ts *todo.Store
	var um *usermemory.Store
	if db != nil {
		fs = familystate.NewStore(db)
		ts = todo.NewStore(db)
		um = usermemory.NewStore(db)
	}

	return &Agent{
		user:         user,
		cfg:          cfg,
		llmClient:    llmClient,
		evaluator:    evaluator,
		classifier:   clf,
		db:           db,
		pool:         deps.Pool,
		skills:       deps.Skills,
		quarantine:   deps.Quarantine,
		scanner:      deps.Scanner,
		scheduler:    deps.Scheduler,
		builtinTools: builtins,
		convID:       convID,
		gateway:      deps.Gateway,
		familyState:  fs,
		todoStore:    ts,
		userMemory:   um,
		webFetcher:   webfetch.Fetch,
		cache:        deps.Cache,
		browserPool:  deps.BrowserPool,
		msgContext:   deps.MsgContext,
	}
}

// Chat processes a single user message and returns a Response.
// Delegates to agentcore.FamilyPipeline for the processing stages.
func (a *Agent) Chat(ctx context.Context, userMessage string, onToken func(string)) (*Response, error) {
	// Save user message before processing
	if err := a.db.SaveMessage(a.convID, a.user.Name, "user", userMessage, "", ""); err != nil {
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
			Role:    m.Role,
			Content: m.Content,
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
		return llm.NewClient(ep.BaseURL, ep.Model, ep.APIKey)
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
	}

	pipeline := agentcore.FamilyPipeline(deps)

	err := pipeline.Run(ctx, turn)

	// Handle policy blocks (not a real error — just a non-allow decision)
	if errors.Is(err, agentcore.ErrPolicyBlock) {
		log.Printf("[agent][%s] cat=%s action=%s", a.user.Name, turn.Category, turn.Policy.Action)
		if err := a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), turn.Policy.Action); err != nil {
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
			if dbErr := a.db.SaveMessage(a.convID, a.user.Name, "assistant", blocked, string(turn.Category), "block"); dbErr != nil {
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
	// - If the output gate soft-blocked and redacted, emit turn.Output
	//   instead of raw tokens to prevent redaction leaks (security fix).
	// - If the output gate allowed unchanged, emit the individual tokens.
	if onToken != nil && len(bufferedTokens) > 0 {
		raw := strings.Join(bufferedTokens, "")
		if blocked, ok := turn.GetMeta("output_blocked"); ok && blocked.(bool) {
			// Hard-blocked: safe message already in turn.Output, no emit.
		} else if turn.Output != raw {
			// Redacted (soft-block): emit the final redacted output, not raw tokens.
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
	if err := a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), "allow"); err != nil {
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
			return usermemory.HandleRecall(ctx, a.userMemory, a.user.Name, category)
		case "builtin__forget_user_memory":
			if a.userMemory == nil {
				return "", fmt.Errorf("user memory not configured")
			}
			category, _ := args["category"].(string)
			label, _ := args["label"].(string)
			return usermemory.HandleForget(ctx, a.userMemory, a.user.Name, category, label)
		case "builtin__list_pending_approvals":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleListPendingApprovals(ctx, deps, args)
		case "builtin__list_users":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleListUsers(ctx, deps, args)
		case "builtin__list_unknown_accounts":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleListUnknownAccounts(ctx, deps, args)
		case "builtin__approve_request":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleApproveRequest(ctx, deps, args)
		case "builtin__deny_request":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleDenyRequest(ctx, deps, args)
		case "builtin__set_user_role":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleSetUserRole(ctx, deps, args)
		case "builtin__link_account":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
			return admin.HandleLinkAccount(ctx, deps, args)
		case "builtin__set_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleSetFamilyFact(ctx, deps, args)
		case "builtin__delete_family_fact":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleDeleteFamilyFact(ctx, deps, args)
		case "builtin__add_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
			return admin.HandleAddFamilyCategory(ctx, deps, args)
		case "builtin__delete_family_category":
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway, FamilyState: a.familyState}
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
			return reminder.HandleAddReminder(ctx, a.db, a.user, a.msgContext.Gateway, a.msgContext.ExternalID, a.msgContext.GroupID, a.msgContext.IsGroup, when, message, forUser)
		default:
			return "", fmt.Errorf("unknown builtin tool: %s", name)
		}
	}
}

func (a *Agent) confinePath(path string) (string, error) {
	if a.cfg.Tools.SandboxRoot == "" {
		return "", fmt.Errorf("sandbox root not configured")
	}
	sandboxRoot := a.cfg.Tools.SandboxRoot
	var err error
	// Ensure sandbox root is absolute and evaluated for symlinks
	if sandboxRoot, err = filepath.EvalSymlinks(filepath.Clean(sandboxRoot)); err != nil {
		return "", fmt.Errorf("invalid sandbox root: %w", err)
	}
	var absPath string
	if filepath.IsAbs(path) {
		if absPath, err = filepath.EvalSymlinks(filepath.Clean(path)); err != nil {
			return "", fmt.Errorf("failed to clean path: %w", err)
		}
	} else {
		if absPath, err = filepath.EvalSymlinks(filepath.Clean(filepath.Join(sandboxRoot, path))); err != nil {
			return "", fmt.Errorf("failed to join and clean path: %w", err)
		}
	}
	// Check that the path is within the sandbox root using filepath.Rel to avoid string prefix issues.
	rel, err := filepath.Rel(sandboxRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("computing relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes sandbox root %q", path, sandboxRoot)
	}
	return absPath, nil
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
	if a.cfg.Tools.SandboxRoot == "" {
		return "", fmt.Errorf("file_write: sandbox root not configured")
	}
	sandboxRoot, err := filepath.EvalSymlinks(filepath.Clean(a.cfg.Tools.SandboxRoot))
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
	}

	subCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	agentID, resultCh, err := a.scheduler.Submit(subCtx, cfg, func(ctx context.Context, cfg subagent.Config) (string, error) {
		return subagent.Execute(ctx, cfg, execDeps)
	})
	if err != nil {
		return "", fmt.Errorf("spawning subagent: %w", err)
	}

	log.Printf("[agent][%s] spawned subagent %s on profile %q (timeout=%ds)", a.user.Name, agentID, profile, timeoutSec)

	select {
	case result := <-resultCh:
		if result.Error != nil {
			return "", fmt.Errorf("subagent %s failed: %w", result.AgentID, result.Error)
		}
		log.Printf("[agent][%s] subagent %s completed (%d chars)", a.user.Name, result.AgentID, len(result.Output))
		return result.Output, nil
	case <-subCtx.Done():
		return "", fmt.Errorf("subagent timed out: %w", subCtx.Err())
	}
}

// handleWebFetch dispatches the builtin__web_fetch tool. Auth boundary:
// validates scheme, enforces the per-host URL allowlist (must be non-empty
// — empty list is treated as deny-all to prevent SSRF into the home LAN),
// and propagates the same allowlist into webfetch.Fetch as a HostValidator
// so it is re-applied to every redirect target. webfetch.Fetch additionally
// blocks private/loopback/link-local IPs at the dialer.
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

	// Empty allowlist = deny-all. The previous "empty means any host"
	// semantic was an SSRF footgun on home networks — a misconfigured
	// deployment could let a jailbroken LLM reach 192.168.1.1/admin or
	// other LAN services. Operators must explicitly list the hosts they
	// want web_fetch to reach.
	if len(cfg.URLAllowlist) == 0 {
		return "", fmt.Errorf("web_fetch denied: tools.web_fetch.url_allowlist is empty (set at least one host to enable)")
	}

	// canonicalHost lowercases the hostname and strips a trailing dot so
	// "EXAMPLE.com" and "example.com." both match an "example.com" entry.
	canonicalHost := func(h string) string {
		return strings.TrimSuffix(strings.ToLower(h), ".")
	}
	hostAllowed := func(host string) bool {
		host = canonicalHost(host)
		for _, allowed := range cfg.URLAllowlist {
			allowed = canonicalHost(strings.TrimSpace(allowed))
			if allowed == "" {
				continue
			}
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				return true
			}
		}
		return false
	}
	if !hostAllowed(u.Hostname()) {
		return "", fmt.Errorf("host %q not in url_allowlist", u.Hostname())
	}

	opts := webfetch.Options{
		MaxBytes: cfg.MaxBytes,
		Timeout:  time.Duration(cfg.TimeoutSec) * time.Second,
		HostValidator: func(host string) error {
			if !hostAllowed(host) {
				return fmt.Errorf("host %q not in url_allowlist", host)
			}
			return nil
		},
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
	if err != nil {
		log.Printf("[agent][%s] web_fetch url=%q err=%v", a.user.Name, rawURL, err)
		return "", err
	}
	if result != nil {
		log.Printf("[agent][%s] web_fetch url=%q status=%d bytes=%d truncated=%v",
			a.user.Name, rawURL, result.StatusCode, result.Bytes, result.Truncated)
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

// handleWebSearch dispatches the builtin__web_search tool. Auth boundary:
// reuses the same per-host URL allowlist as web_fetch (the search endpoint
// host must be in tools.web_fetch.url_allowlist) and the same private-net
// policy. The model gets a compact title/url/snippet listing instead of
// the raw 4-16KB SearXNG JSON.
func (a *Agent) handleWebSearch(ctx context.Context, args map[string]any) (string, error) {
	cfg := a.cfg.Tools.WebSearch
	if !cfg.Enabled {
		return "", fmt.Errorf("web_search is disabled in this deployment")
	}
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("web_search requires a 'query' argument")
	}

	maxResults := readIntArg(args, "max_results", cfg.MaxResults)

	fcfg := a.cfg.Tools.WebFetch
	canonicalHost := func(h string) string {
		return strings.TrimSuffix(strings.ToLower(h), ".")
	}
	hostAllowed := func(host string) bool {
		host = canonicalHost(host)
		for _, allowed := range fcfg.URLAllowlist {
			allowed = canonicalHost(strings.TrimSpace(allowed))
			if allowed == "" {
				continue
			}
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				return true
			}
		}
		return false
	}

	hits, err := websearch.Search(ctx, query, websearch.Options{
		Endpoint:   cfg.Endpoint,
		MaxResults: maxResults,
		Timeout:    time.Duration(cfg.TimeoutSec) * time.Second,
		HostValidator: func(host string) error {
			if !hostAllowed(host) {
				return fmt.Errorf("host %q not in url_allowlist", host)
			}
			return nil
		},
		AllowPrivateNetworks: !fcfg.BlockPrivateNetworks,
	})
	if err != nil {
		return "", err
	}
	log.Printf("[agent][%s] web_search query=%q hits=%d", a.user.Name, query, len(hits))
	return websearch.FormatHits(hits), nil
}

// handleBrowser dispatches all builtin__browser_* tools. Auth boundary:
// reuses tools.web_fetch.url_allowlist as the navigation host gate (only
// browser_navigate consults it; subsequent click/fill/extract act on the
// current page so they inherit the validated host).
func (a *Agent) handleBrowser(ctx context.Context, toolName string, args map[string]any) (string, error) {
	if a.browserPool == nil {
		return "", fmt.Errorf("%s: browser tools are disabled in this deployment", toolName)
	}
	fcfg := a.cfg.Tools.WebFetch
	canonicalHost := func(h string) string {
		return strings.TrimSuffix(strings.ToLower(h), ".")
	}
	hostAllowed := func(host string) bool {
		host = canonicalHost(host)
		for _, allowed := range fcfg.URLAllowlist {
			allowed = canonicalHost(strings.TrimSpace(allowed))
			if allowed == "" {
				continue
			}
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				return true
			}
		}
		return false
	}
	out, err := a.browserPool.Exec(ctx, browser.ExecInput{
		User:     a.user.Name,
		ToolName: toolName,
		Args:     args,
		HostCheck: func(host string) error {
			if !hostAllowed(host) {
				return fmt.Errorf("host %q not in url_allowlist", host)
			}
			return nil
		},
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
			_ = a.db.LogAudit(ctx, a.user.Name, a.gateway, "builtin__propose_family_fact", auditArgs)
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
	if _, err := a.db.UpsertApproval(approval); err != nil {
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

// buildMessages assembles the LLM message list from history + system prompt.
func (a *Agent) buildMessages(ctx context.Context, history []*store.Message, currentMessage string) []llm.Message {
	var msgs []llm.Message

	var systemPrompt string
	if a.cfg.LLM.SystemPrompt != "" {
		// Operator override — keep legacy behavior verbatim, but always
		// append the behavioral guardrails (tool-call format + grounding
		// rules) so deployments that set a custom system_prompt still get
		// the leak/hallucination protection.
		systemPrompt = a.cfg.LLM.SystemPrompt + "\n\n" + ageContextPrompt(a.user) + "\n\n" + prompt.BehavioralRules()
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
		var builtinNames []string
		for _, t := range a.builtinTools {
			if !t.AllowedForRole(a.user.Role) {
				continue
			}
			bare := strings.TrimPrefix(t.Name, "builtin__")
			if a.evaluator != nil {
				dec, err := a.evaluator.EvaluateToolCall(context.Background(), policy.ToolCallInput{
					User:     policy.UserInput{Role: a.user.Role, AgeGroup: a.user.AgeGroup, Name: a.user.Name},
					ToolName: bare,
				})
				if err != nil || !dec.Allow {
					continue
				}
			}
			builtinNames = append(builtinNames, bare)
		}

systemPrompt = prompt.Build(prompt.BuildContext{
	Cfg:          a.cfg,
	User:         a.user,
	Skills:       skillNames,
	BuiltinTools: builtinNames,
	// Gateway and HardBlocked left empty for now — wired by future PRs
	// that thread gateway and policy info through Agent.
})
	}

	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})

	for _, m := range history {
		if m.PolicyAction == "allow" || m.PolicyAction == "" {
			msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" {
		msgs = append(msgs, llm.Message{Role: "user", Content: currentMessage})
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
		"builtin__link_account":
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
