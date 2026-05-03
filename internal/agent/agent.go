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
	"net/url"
	"strings"
	"time"

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
	user       *config.UserConfig
	cfg        *config.Config
	llmClient  *llm.Client
	evaluator  *policy.Evaluator
	classifier *classifier.Classifier
	db         *store.DB
	pool         *mcp.Pool
	skills       []*skillbridge.Skill
	quarantine   *skillbridge.Quarantine
	scanner      skillbridge.Scanner
	oauthStore   *llm.OAuthStore
	scheduler    *subagent.Scheduler
	builtinTools []agentcore.Tool // tools to inject onto every Turn
	convID       string

	// webFetcher is the function used by handleWebFetch. Defaults to
	// webfetch.Fetch in NewAgent. Tests can swap in a stub to assert the
	// allowlist/scheme/empty-list gates without making real HTTP calls.
	webFetcher func(ctx context.Context, rawURL string, opts webfetch.Options) (*webfetch.Result, error)
}

// AgentDeps holds optional dependencies for an Agent. All fields are
// safe to leave nil — the Agent degrades gracefully (no MCP tools,
// no skills, no scanning, no subagents).
type AgentDeps struct {
	Pool         *mcp.Pool
	Skills       []*skillbridge.Skill
	Quarantine   *skillbridge.Quarantine
	Scanner      skillbridge.Scanner
	OAuthStore   *llm.OAuthStore
	Scheduler    *subagent.Scheduler
	BuiltinTools []agentcore.Tool
}

// NewAgent creates an Agent for the given user. Optional dependencies
// (MCP pool, skills, scanner, scheduler, etc.) are passed in deps —
// any field left as zero value disables that capability for this Agent.
func NewAgent(user *config.UserConfig, cfg *config.Config, llmClient *llm.Client,
	evaluator *policy.Evaluator, clf *classifier.Classifier, db *store.DB,
	deps AgentDeps) *Agent {

	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(user.Name + ":" + day))
	convID := hex.EncodeToString(h[:8])

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
		oauthStore:   deps.OAuthStore,
		scheduler:    deps.Scheduler,
		builtinTools: deps.BuiltinTools,
		convID:       convID,
		webFetcher:   webfetch.Fetch,
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
	messages := a.buildMessages(history, userMessage)

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

	// Resolve LLM profile — supports both API key and OAuth auth
	clientFactory := func(t *agentcore.Turn) *llm.Client {
		ep := a.cfg.LLMEndpointFor(t.User)
		if ep.BaseURL == "" || ep.Model == "" {
			return nil
		}
		if ep.AuthType == "oauth" && a.oauthStore != nil {
			return llm.NewOAuthClient(ep.BaseURL, ep.Model, a.oauthStore, "anthropic")
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
		OAuthStore:  a.oauthStore,
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

	hostAllowed := func(host string) bool {
		for _, allowed := range cfg.URLAllowlist {
			allowed = strings.TrimSpace(allowed)
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
	}
	// Caller-supplied max_bytes can only further restrict the cap, never raise it.
	if v, ok := args["max_bytes"].(float64); ok && v > 0 {
		caller := int64(v)
		if opts.MaxBytes == 0 || caller < opts.MaxBytes {
			opts.MaxBytes = caller
		}
	}

	result, err := a.webFetcher(ctx, rawURL, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("URL: %s\nStatus: %d\nContent-Type: %s\nTruncated: %v\n\n%s",
		result.URL, result.StatusCode, result.ContentType, result.Truncated, result.Text), nil
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
func (a *Agent) buildMessages(history []*store.Message, currentMessage string) []llm.Message {
	var msgs []llm.Message

	ep := a.cfg.LLMEndpointFor(a.user)

	var systemPrompt string
	if a.cfg.LLM.SystemPrompt != "" {
		// Operator override — keep legacy behavior verbatim.
		systemPrompt = a.cfg.LLM.SystemPrompt + "\n\n" + ageContextPrompt(a.user)
		if len(a.skills) > 0 {
			if sp := skillbridge.LoadForPrompt(a.skills); sp != "" {
				systemPrompt += "\n\n" + sp
			}
		}
		if ep.AuthType == "oauth" {
			systemPrompt = llm.ClaudeCodeSystemPrefix + "\n\n" + systemPrompt
		}
	} else {
		// Default — use the structured PromptBuilder.
		skillNames := make([]string, 0, len(a.skills))
		for _, sk := range a.skills {
			if sk != nil {
				skillNames = append(skillNames, sk.Name)
			}
		}
		var builtinNames []string
		for _, t := range a.builtinTools {
			if t.AllowedForRole(a.user.Role) {
				builtinNames = append(builtinNames, strings.TrimPrefix(t.Name, "builtin__"))
			}
		}

		systemPrompt = prompt.Build(prompt.BuildContext{
			Cfg:          a.cfg,
			User:         a.user,
			Skills:       skillNames,
			OAuth:        ep.AuthType == "oauth",
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

// ApprovalID generates a deterministic approval ID for user+category+day.
func ApprovalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}
