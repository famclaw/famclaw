// Package agent implements the FamClaw conversation loop.
// Each user gets an isolated conversation with policy enforcement at every turn.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

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
	pool       *mcp.Pool              // nil-safe — tool calls skipped if nil
	skills     []*skillbridge.Skill   // injected into system prompt
	convID     string
}

// SetPool attaches an MCP tool pool to the agent.
func (a *Agent) SetPool(p *mcp.Pool) { a.pool = p }

// SetSkills sets the skills to inject into the system prompt.
func (a *Agent) SetSkills(skills []*skillbridge.Skill) { a.skills = skills }

// NewAgent creates an Agent for the given user.
func NewAgent(user *config.UserConfig, cfg *config.Config, llmClient *llm.Client,
	evaluator *policy.Evaluator, clf *classifier.Classifier, db *store.DB) *Agent {

	// Conversation ID: scoped to user + day (new conversation each day)
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
// onToken is called for each streamed LLM token (can be nil for non-streaming).
func (a *Agent) Chat(ctx context.Context, userMessage string, onToken func(string)) (*Response, error) {
	// ── 1. Classify query ─────────────────────────────────────────────────────
	cat := a.classifier.Classify(userMessage)

	// ── 2. Evaluate policy ────────────────────────────────────────────────────
	requestID := ApprovalID(a.user.Name, string(cat))
	approvals, _ := a.db.AllApprovalsForOPA()

	decision, err := a.evaluator.Evaluate(ctx, policy.Input{
		User: policy.UserInput{
			Role:     a.user.Role,
			AgeGroup: a.user.AgeGroup,
			Name:     a.user.Name,
		},
		Query:     policy.QueryInput{Category: string(cat), Text: userMessage},
		RequestID: requestID,
		Approvals: approvals,
	})
	if err != nil {
		log.Printf("[agent][%s] OPA error: %v", a.user.Name, err)
		decision = policy.Decision{Action: "block", Reason: "Policy evaluation error"}
	}

	log.Printf("[agent][%s] cat=%s action=%s", a.user.Name, cat, decision.Action)

	// ── 3. Save user message ─────────────────────────────────────────────────
	_ = a.db.SaveMessage(a.convID, a.user.Name, "user", userMessage, string(cat), decision.Action)

	// ── 4. Handle non-allow decisions without calling LLM ────────────────────
	if decision.Action != "allow" {
		msg := a.policyMessage(decision)
		_ = a.db.SaveMessage(a.convID, a.user.Name, "assistant", msg, string(cat), decision.Action)
		return &Response{Content: msg, PolicyAction: decision.Action, Category: string(cat)}, nil
	}

	// ── 5. Build conversation context ─────────────────────────────────────────
	history, err := a.db.GetConversationHistory(a.convID, 20)
	if err != nil {
		history = nil
	}

	messages := a.buildMessages(history, userMessage)

	// ── 6. Stream LLM response ────────────────────────────────────────────────
	if a.cfg.LLM.BaseURL == "" {
		return nil, fmt.Errorf("LLM not configured — open the web UI to set up your AI backend")
	}
	model := a.cfg.ModelFor(a.user)
	if model == "" {
		return nil, fmt.Errorf("LLM model not configured — open the web UI settings")
	}
	client := llm.NewClient(a.cfg.LLM.BaseURL, model, a.cfg.LLM.APIKey)

	response, err := client.Chat(ctx, messages, a.cfg.LLM.Temperature, a.cfg.LLM.MaxResponseTokens, onToken)
	if err != nil {
		return nil, fmt.Errorf("LLM error: %w", err)
	}

	// ── 6b. Tool call loop (MCP) ─────────────────────────────────────────────
	if a.pool != nil {
		response, err = a.toolCallLoop(ctx, client, messages, response, cat)
		if err != nil {
			return nil, fmt.Errorf("tool call error: %w", err)
		}
	}

	// ── 7. Save assistant response ───────────────────────────────────────────
	_ = a.db.SaveMessage(a.convID, a.user.Name, "assistant", response, string(cat), "allow")

	return &Response{
		Content:      response,
		PolicyAction: "allow",
		Category:     string(cat),
		Streamed:     onToken != nil,
	}, nil
}

// buildMessages assembles the LLM message list from history + system prompt.
func (a *Agent) buildMessages(history []*store.Message, currentMessage string) []llm.Message {
	var msgs []llm.Message

	// System prompt
	systemPrompt := a.cfg.LLM.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt(a.user)
	} else {
		systemPrompt += "\n\n" + ageContextPrompt(a.user)
	}

	// Inject skill descriptions into system prompt
	if len(a.skills) > 0 {
		skillPrompt := skillbridge.LoadForPrompt(a.skills)
		if skillPrompt != "" {
			systemPrompt += "\n\n" + skillPrompt
		}
	}

	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})

	// History (only allowed messages — don't include blocked turns)
	for _, m := range history {
		if m.PolicyAction == "allow" || m.PolicyAction == "" {
			msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
		}
	}

	// Current message (already in history, but we add it explicitly for clarity)
	// Only add if not already the last message in history
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		msgs = append(msgs, llm.Message{Role: "user", Content: currentMessage})
	}

	return msgs
}

func (a *Agent) policyMessage(d policy.Decision) string {
	switch d.Action {
	case "block":
		return fmt.Sprintf("I'm sorry, I can't help with that topic. %s", d.Reason)
	case "request_approval":
		return "I've asked a parent to approve this topic for you. They'll get a notification — once they approve, just ask me again! 😊"
	case "pending":
		return "A parent has already been notified about this request. Once they approve, you can ask me! ⏳"
	default:
		return "I'm unable to answer that right now."
	}
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
		return fmt.Sprintf("You are talking with %s, who is %s years old (8-12). Be friendly and educational. Explain things clearly without being condescending.", user.DisplayName, "between 8 and 12")
	case "age_13_17":
		return fmt.Sprintf("You are talking with %s, a teenager. Be respectful and treat them as a capable young adult. You can discuss more complex topics but remain age-appropriate.", user.DisplayName)
	default:
		return ""
	}
}

// toolCallLoop executes MCP tool calls from LLM responses, up to MaxToolCallIterations.
func (a *Agent) toolCallLoop(ctx context.Context, client *llm.Client, messages []llm.Message, initialResponse string, cat classifier.Category) (string, error) {
	// Get the full message with tool calls (non-streaming)
	msg, err := client.ChatMessage(ctx, messages, a.cfg.LLM.Temperature, a.cfg.LLM.MaxResponseTokens)
	if err != nil || msg == nil || len(msg.ToolCalls) == 0 {
		return initialResponse, nil // no tool calls, return original
	}

	for i := 0; i < mcp.MaxToolCallIterations; i++ {
		if len(msg.ToolCalls) == 0 {
			break
		}

		// Append assistant message with tool calls
		messages = append(messages, *msg)

		// Execute each tool call
		for _, tc := range msg.ToolCalls {
			log.Printf("[agent][%s] tool_call: %s(%v)", a.user.Name, tc.Function.Name, tc.Function.Arguments)

			if !a.pool.HasTool(tc.Function.Name) {
				messages = append(messages, llm.Message{
					Role:    "tool",
					Content: fmt.Sprintf("Error: unknown tool %q", tc.Function.Name),
				})
				continue
			}

			result, err := a.pool.CallTool(ctx, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				messages = append(messages, llm.Message{
					Role:    "tool",
					Content: fmt.Sprintf("Error calling %s: %v", tc.Function.Name, err),
				})
				continue
			}

			// Extract text from MCP result
			var toolText string
			if result != nil && len(result.Content) > 0 {
				resultJSON, _ := json.Marshal(result.Content)
				toolText = string(resultJSON)
			}
			messages = append(messages, llm.Message{
				Role:    "tool",
				Content: toolText,
			})
		}

		// Call LLM again with tool results
		msg, err = client.ChatMessage(ctx, messages, a.cfg.LLM.Temperature, a.cfg.LLM.MaxResponseTokens)
		if err != nil {
			return "", fmt.Errorf("LLM error in tool loop iteration %d: %w", i+1, err)
		}
	}

	return msg.Content, nil
}

// ApprovalID generates a deterministic approval ID for user+category+day.
func ApprovalID(userName, category string) string {
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(userName + ":" + category + ":" + day))
	return hex.EncodeToString(h[:8])
}
