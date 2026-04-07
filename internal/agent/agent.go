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
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/mcp"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/skillbridge"
	"github.com/famclaw/famclaw/internal/store"
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
	pool       *mcp.Pool
	skills     []*skillbridge.Skill
	quarantine *skillbridge.Quarantine
	scanner    skillbridge.Scanner
	convID     string
}

// SetPool attaches an MCP tool pool to the agent.
func (a *Agent) SetPool(p *mcp.Pool) { a.pool = p }

// SetSkills sets the skills to inject into the system prompt.
func (a *Agent) SetSkills(skills []*skillbridge.Skill) { a.skills = skills }

// SetQuarantine attaches the quarantine store for runtime tool filtering.
func (a *Agent) SetQuarantine(q *skillbridge.Quarantine) { a.quarantine = q }

// SetScanner attaches the security scanner for async runtime scanning.
func (a *Agent) SetScanner(s skillbridge.Scanner) { a.scanner = s }

// NewAgent creates an Agent for the given user.
func NewAgent(user *config.UserConfig, cfg *config.Config, llmClient *llm.Client,
	evaluator *policy.Evaluator, clf *classifier.Classifier, db *store.DB) *Agent {

	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(user.Name + ":" + day))
	convID := hex.EncodeToString(h[:8])

	return &Agent{
		user:       user,
		cfg:        cfg,
		llmClient:  llmClient,
		evaluator:  evaluator,
		classifier: clf,
		db:         db,
		convID:     convID,
	}
}

// Chat processes a single user message and returns a Response.
// Delegates to agentcore.FamilyPipeline for the processing stages.
func (a *Agent) Chat(ctx context.Context, userMessage string, onToken func(string)) (*Response, error) {
	// Save user message before processing
	_ = a.db.SaveMessage(a.convID, a.user.Name, "user", userMessage, "", "")

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

	// Resolve LLM profile
	clientFactory := func(t *agentcore.Turn) *llm.Client {
		baseURL, model, apiKey := a.cfg.LLMClientFor(t.User)
		if baseURL == "" || model == "" {
			return nil
		}
		return llm.NewClient(baseURL, model, apiKey)
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
		_ = a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), turn.Policy.Action)
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
	_ = a.db.SaveMessage(a.convID, a.user.Name, "assistant", turn.Output, string(turn.Category), "allow")

	return &Response{
		Content:      turn.Output,
		PolicyAction: "allow",
		Category:     string(turn.Category),
		Streamed:     turn.Streamed,
	}, nil
}

// buildMessages assembles the LLM message list from history + system prompt.
func (a *Agent) buildMessages(history []*store.Message, currentMessage string) []llm.Message {
	var msgs []llm.Message

	systemPrompt := a.cfg.LLM.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt(a.user)
	} else {
		systemPrompt += "\n\n" + ageContextPrompt(a.user)
	}

	if len(a.skills) > 0 {
		skillPrompt := skillbridge.LoadForPrompt(a.skills)
		if skillPrompt != "" {
			systemPrompt += "\n\n" + skillPrompt
		}
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

// outputBlockedPatterns kept for backward compatibility with existing tests.
var outputBlockedPatterns = []string{
	"suicide", "kill yourself", "self-harm", "cutting yourself",
	"pornography", "sexual intercourse", "explicit content",
	"racial slur", "ethnic cleansing", "white supremac",
	"how to make a bomb", "how to steal", "how to hack",
}

// filterOutput returns true if the LLM response contains blocked content.
// Kept for backward compatibility with existing tests — the pipeline uses StageOutputFilter.
func filterOutput(response string) bool {
	lower := strings.ToLower(response)
	for _, pattern := range outputBlockedPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// ApprovalID generates a deterministic approval ID for user+category+day.
func ApprovalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}
