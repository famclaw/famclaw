package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/gateway"
	"github.com/famclaw/famclaw/internal/store"
	"github.com/famclaw/famclaw/internal/subagent"
)

// researchStatusToolName is the namespaced builtin name for the status tool.
const researchStatusToolName = "builtin__research_status"

// ResearchStatusTool returns the agentcore.Tool definition for the
// research_status tool. It is always-on (Roles nil = all roles) so that any
// user can inspect tasks they spawned; the handler scopes results to the
// requesting user only. Parents get allow-all via policy regardless.
func ResearchStatusTool() agentcore.Tool {
	return agentcore.Tool{
		Name: researchStatusToolName,
		Description: "Check the status of a previously-spawned research task. If agent_id is " +
			"omitted, lists your recent research tasks (running, completed, failed, " +
			"timed_out) with their results — even when the result could not be delivered " +
			"to your chat.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The research task id (e.g. agent-1) to look up. Omit to list recent tasks.",
				},
			},
		},
		Source: "builtin",
	}
}

// handleResearchStatus is the handler for the research_status tool.
func (a *Agent) handleResearchStatus(ctx context.Context, args map[string]any) (string, error) {
	if a.db == nil {
		return "No database available to look up research status.", nil
	}

	agentID, _ := args["agent_id"].(string)
	var rows []*store.ResearchStatus
	if agentID != "" {
		s, err := a.db.GetResearchStatus(ctx, a.user.Name, agentID)
		if err != nil {
			return "", fmt.Errorf("research_status: %w", err)
		}
		if s == nil {
			return fmt.Sprintf("No research task %q found for you. Use `spawn_agent` to start one.", agentID), nil
		}
		rows = []*store.ResearchStatus{s}
	} else {
		more, err := a.db.ListResearchStatusByUser(ctx, a.user.Name, 20)
		if err != nil {
			return "", fmt.Errorf("research_status: %w", err)
		}
		rows = more
	}

	if len(rows) == 0 {
		return "You have no research tasks on record. Use `spawn_agent` to start one.", nil
	}

	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(formatResearchStatus(r))
	}
	return b.String(), nil
}

// formatResearchStatus renders a single research status record for display.
func formatResearchStatus(r *store.ResearchStatus) string {
	mark := "delivered ✅"
	if !r.Delivered {
		mark = "NOT delivered ❌"
		if r.DeliveryErr != "" {
			mark += " — " + r.DeliveryErr
		}
	}
	dur := ""
	if r.EndedAt != nil {
		dur = fmt.Sprintf(" (%s)", r.EndedAt.Sub(r.StartedAt).Round(time.Second).String())
	}
	return fmt.Sprintf("%s · %s%s · %s\n%s", r.AgentID, r.Status, dur, mark, r.Deliverable)
}

// chatIDOf computes the gateway chat id to send to from a message context.
func chatIDOf(m gateway.MsgContext) string {
	if m.IsGroup && m.GroupID != "" {
		return m.GroupID
	}
	return m.ExternalID
}

// buildResearchDeliverable formats the terminal message a research task
// produces for the originating conversation, based on its outcome.
func buildResearchDeliverable(agentID string, state store.ResearchStatusState, resultText string, timeoutSec int) string {
	switch state {
	case store.ResearchStatusTimedOut:
		return fmt.Sprintf("⏰ Research task %s timed out after %d seconds.", agentID, timeoutSec)
	case store.ResearchStatusFailed:
		return fmt.Sprintf("❌ Research task %s failed: %s", agentID, resultText)
	case store.ResearchStatusCompleted:
		return fmt.Sprintf("🔬 Research task %s completed:\n%s", agentID, resultText)
	default:
		return fmt.Sprintf("📋 Research task %s: %s", agentID, resultText)
	}
}

// classifySubagentResult maps a completed subagent result + its context state
// to a research status state and the result text to persist/display. A
// deadline-exceeded error takes precedence over a generic error so timeouts
// are classified as timed_out rather than failed.
func classifySubagentResult(result subagent.Result, subCtx context.Context) (store.ResearchStatusState, string) {
	if result.Error == nil {
		return store.ResearchStatusCompleted, result.Output
	}
	if subCtx.Err() != nil && errors.Is(subCtx.Err(), context.DeadlineExceeded) {
		return store.ResearchStatusTimedOut, result.Error.Error()
	}
	return store.ResearchStatusFailed, result.Error.Error()
}

// finalizeResearch persists the terminal status of a research task, attempts
// to deliver the result to the originating conversation, records the delivery
// outcome, and surfaces the result into the conversation history so the user
// sees it on their next message even if the channel delivery failed.
func (a *Agent) finalizeResearch(ctx context.Context, agentID string, state store.ResearchStatusState, resultText string, timeoutSec int, prompt string, msgCtx gateway.MsgContext) {
	deliverable := buildResearchDeliverable(agentID, state, resultText, timeoutSec)

	// Attempt delivery first so the terminal status can record the outcome.
	delivered, sendErr := a.deliverResultToOrigin(agentID, deliverable, msgCtx)
	if sendErr != nil {
		// The failure is already logged inside deliverResultToOrigin; here we
		// only surface it to the status record and conversation history so it
		// is never silently swallowed.
		log.Printf("[agent][%s] research %s delivery failed: %v", a.user.Name, agentID, sendErr)
	}

	now := a.now()
	status := store.ResearchStatus{
		AgentID:    agentID,
		UserName:   a.user.Name,
		Prompt:     prompt,
		Status:     state,
		Result:     resultText,
		Deliverable: deliverable,
		Delivered:  delivered,
		DeliveryErr: errString(sendErr),
		Gateway:    msgCtx.Gateway,
		ChatID:     chatIDOf(msgCtx),
		StartedAt:  now, // created_at/started_at are preserved from the running insert on conflict
		EndedAt:    &now,
	}
	if a.db != nil {
		if err := a.db.UpsertResearchStatus(&status); err != nil {
			log.Printf("[agent][%s] save research status %s: %v", a.user.Name, agentID, err)
		}
	}

	// Surface the outcome into the conversation history. This guarantees the
	// user stops seeing "still working" even when channel delivery fails: the
	// result/failure lands in their conversation record and is reloaded on the
	// next message. Safe no-op when there is no DB or conversation id.
	if a.db != nil && a.convID != "" {
		if err := a.db.SaveMessage(a.convID, a.user.Name, "assistant", deliverable, "", ""); err != nil {
			log.Printf("[agent][%s] save research result to conversation %s: %v", a.user.Name, a.convID, err)
		}
	}
}

// persistResearchStart records the "running" status for a research task at the
// moment it is spawned. No-op when there is no DB.
func (a *Agent) persistResearchStart(agentID, prompt string, timeoutSec int, msgCtx gateway.MsgContext) {
	if a.db == nil {
		return
	}
	now := a.now()
	status := store.ResearchStatus{
		AgentID:    agentID,
		UserName:   a.user.Name,
		Prompt:     prompt,
		Status:     store.ResearchStatusRunning,
		StartedAt:  now,
		CreatedAt:  now,
		Gateway:    msgCtx.Gateway,
		ChatID:     chatIDOf(msgCtx),
	}
	if err := a.db.UpsertResearchStatus(&status); err != nil {
		log.Printf("[agent][%s] save research running %s: %v", a.user.Name, agentID, err)
	}
}

// errString returns "" for a nil error, otherwise err.Error().
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
