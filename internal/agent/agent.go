// Package agent implements the FamClaw conversation loop.
// Each user gets an isolated conversation with policy enforcement at every turn.
// Delegates to agentcore.Pipeline for the actual processing stages.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agent/tools/admin"
	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/prompt"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
	"github.com/famclaw/famclaw/internal/toolcache"
	"github.com/famclaw/famclaw/internal/webfetch"
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

	// webFetcher is the function used by handleWebFetch. Defaults to
	// webfetch.Fetch in NewAgent. Tests can swap in a stub to assert the
	// allowlist/scheme/empty-list gates without making real HTTP calls.
	webFetcher func(ctx context.Context, rawURL string, opts webfetch.Options) (*webfetch.Result, error)

	// cache is the tool-result spillover store. When non-nil, large
	// handleWebFetch (and other large-result) tool replies are written
	// to the cache and only a budget-sized head slice is returned to the
	// LLM. When nil, the legacy inline-everything path runs.
	cache *toolcache.Cache
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
	Gateway      string // gateway name (telegram, discord, web, etc.) for admin tool audit logs
	Cache        *toolcache.Cache // tool-result spillover cache; nil disables spillover (legacy inline path)
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
		webFetcher:   webfetch.Fetch,
		cache:        deps.Cache,
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
		OnToken:       onToken,
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
		case "builtin__tool_result_more":
			return a.handleToolResultMore(ctx, args)
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
			deps := admin.Deps{DB: a.db, Cfg: a.cfg, Actor: a.user.Name, Gateway: a.gateway}
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
		default:
			return "", fmt.Errorf("unknown builtin tool: %s", name)
		}
	}
}

// Subagent timeout defaults / caps (in seconds).
const (
	subagentDefaultTimeoutSec = 300  // 5 minutes
	subagentMaxTimeoutSec     = 1800 // 30 minutes
)

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

	cfg := subagent.Config{
		Prompt:     prompt,
		LLMProfile: profile,
		MaxTurns:   maxTurns,
		Tools:      allowTools,
		DenyTools:  denyTools,
	}

	execDeps := subagent.ExecutorDeps{
		Pool:        a.pool,
		Config:      a.cfg,
		Temperature: a.cfg.LLM.Temperature,
		MaxTokens:   a.cfg.LLM.MaxResponseTokens,
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
		return "", err
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
