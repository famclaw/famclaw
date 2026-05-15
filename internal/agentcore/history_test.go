package agentcore

import (
	"strings"
	"testing"
)

func TestRebuildUserMessage_AllSectionsPresent(t *testing.T) {
	history := []HistoryItem{
		{
			StepNum:  1,
			Eval:     "navigated successfully",
			Memory:   "user wants TPA→MSY flights",
			NextGoal: "fill origin field",
			Results:  []string{"[clicked search bar]", "[page loaded]"},
		},
		{
			StepNum:  2,
			Eval:     "origin filled",
			Memory:   "origin TPA set",
			NextGoal: "fill destination",
			Results:  []string{"[filled e16 = TPA]"},
		},
	}
	state := AgentState{
		UserRequest: "find me flights TPA→MSY June 10–14",
		Plan:        []string{"[x] navigate to flight search", "[>] fill destination", "[ ] set dates"},
		StepInfo:    "Step 3 of 25 max. Today: 2026-05-14",
	}
	browser := &BrowserState{
		URL:       "https://www.google.com/travel/flights",
		Title:     "Google Flights",
		PageStats: "23 interactive, 87 total",
		Snapshot:  "[1] origin input",
	}

	got := RebuildUserMessage(history, state, browser)

	anchors := []string{
		"<agent_history>",
		"<step_1>",
		"<step_2>",
		"</agent_history>",
		"<agent_state>",
		"<user_request>",
		"<plan>",
		"<step_info>",
		"<browser_state>",
		"URL:",
		"Title:",
	}
	for _, anchor := range anchors {
		if !strings.Contains(got, anchor) {
			t.Errorf("expected output to contain %q, got:\n%s", anchor, got)
		}
	}
}

func TestRebuildUserMessage_NoHistory(t *testing.T) {
	state := AgentState{UserRequest: "hello"}

	t.Run("nil history", func(t *testing.T) {
		got := RebuildUserMessage(nil, state, nil)
		if strings.Contains(got, "agent_history") {
			t.Errorf("expected no agent_history section, got:\n%s", got)
		}
		if strings.Contains(got, "step_") {
			t.Errorf("expected no step_ tags, got:\n%s", got)
		}
		if !strings.Contains(got, "<agent_state>") {
			t.Errorf("expected agent_state section, got:\n%s", got)
		}
		if !strings.Contains(got, "<user_request>") {
			t.Errorf("expected user_request, got:\n%s", got)
		}
	})

	t.Run("empty history", func(t *testing.T) {
		got := RebuildUserMessage([]HistoryItem{}, state, nil)
		if strings.Contains(got, "agent_history") {
			t.Errorf("expected no agent_history section, got:\n%s", got)
		}
		if !strings.Contains(got, "<agent_state>") {
			t.Errorf("expected agent_state section, got:\n%s", got)
		}
	})
}

func TestRebuildUserMessage_NoBrowser(t *testing.T) {
	state := AgentState{UserRequest: "hello"}
	got := RebuildUserMessage(nil, state, nil)
	if strings.Contains(got, "browser_state") {
		t.Errorf("expected no browser_state section, got:\n%s", got)
	}
}

func TestRebuildUserMessage_EmptyResults(t *testing.T) {
	history := []HistoryItem{
		{StepNum: 1, Eval: "ok", Memory: "mem", NextGoal: "next", Results: nil},
	}
	state := AgentState{UserRequest: "test"}

	got := RebuildUserMessage(history, state, nil)
	if !strings.Contains(got, "results: (none)") {
		t.Errorf("expected 'results: (none)', got:\n%s", got)
	}
}

func TestRebuildUserMessage_EmptyFields(t *testing.T) {
	history := []HistoryItem{
		{StepNum: 1, Eval: "", Memory: "", NextGoal: "", Results: nil},
	}
	state := AgentState{UserRequest: "test"}

	got := RebuildUserMessage(history, state, nil)
	if !strings.Contains(got, "eval: (n/a)") {
		t.Errorf("expected 'eval: (n/a)', got:\n%s", got)
	}
	if !strings.Contains(got, "memory: (n/a)") {
		t.Errorf("expected 'memory: (n/a)', got:\n%s", got)
	}
	if !strings.Contains(got, "next_goal: (n/a)") {
		t.Errorf("expected 'next_goal: (n/a)', got:\n%s", got)
	}
}

func TestRebuildUserMessage_NoPlan(t *testing.T) {
	state := AgentState{UserRequest: "test", Plan: nil}
	got := RebuildUserMessage(nil, state, nil)
	if strings.Contains(got, "<plan>") {
		t.Errorf("expected no <plan> section, got:\n%s", got)
	}
}

func TestRebuildUserMessage_NoStepInfo(t *testing.T) {
	state := AgentState{UserRequest: "test", StepInfo: ""}
	got := RebuildUserMessage(nil, state, nil)
	if strings.Contains(got, "step_info") {
		t.Errorf("expected no step_info, got:\n%s", got)
	}
}

func TestRebuildUserMessage_StepNumbering(t *testing.T) {
	history := []HistoryItem{
		{StepNum: 5, Eval: "e5", Memory: "m5", NextGoal: "n5", Results: []string{"r5"}},
		{StepNum: 6, Eval: "e6", Memory: "m6", NextGoal: "n6", Results: []string{"r6"}},
	}
	state := AgentState{UserRequest: "test"}

	got := RebuildUserMessage(history, state, nil)
	if !strings.Contains(got, "<step_5>") {
		t.Errorf("expected <step_5>, got:\n%s", got)
	}
	if !strings.Contains(got, "<step_6>") {
		t.Errorf("expected <step_6>, got:\n%s", got)
	}
}

func TestRebuildUserMessage_TrailingNewline(t *testing.T) {
	state := AgentState{UserRequest: "test"}
	got := RebuildUserMessage(nil, state, nil)
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected output to end with newline, got:\n%q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("expected exactly one trailing newline, got double newline at end")
	}
}

func TestParseSelfSummary_AllThreePresent(t *testing.T) {
	input := `<eval>navigated OK</eval>
<memory>user wants TPA→MSY</memory>
<next_goal>fill origin field</next_goal>`
	eval, memory, nextGoal := ParseSelfSummary(input)
	if eval != "navigated OK" {
		t.Errorf("eval: got %q, want %q", eval, "navigated OK")
	}
	if memory != "user wants TPA→MSY" {
		t.Errorf("memory: got %q, want %q", memory, "user wants TPA→MSY")
	}
	if nextGoal != "fill origin field" {
		t.Errorf("next_goal: got %q, want %q", nextGoal, "fill origin field")
	}
}

func TestParseSelfSummary_NoTags(t *testing.T) {
	eval, memory, nextGoal := ParseSelfSummary("no tags here at all")
	if eval != "" {
		t.Errorf("eval: got %q, want empty", eval)
	}
	if memory != "" {
		t.Errorf("memory: got %q, want empty", memory)
	}
	if nextGoal != "" {
		t.Errorf("next_goal: got %q, want empty", nextGoal)
	}
}

func TestParseSelfSummary_OnlyEval(t *testing.T) {
	eval, memory, nextGoal := ParseSelfSummary("<eval>clicked button</eval>")
	if eval != "clicked button" {
		t.Errorf("eval: got %q, want %q", eval, "clicked button")
	}
	if memory != "" {
		t.Errorf("memory: got %q, want empty", memory)
	}
	if nextGoal != "" {
		t.Errorf("next_goal: got %q, want empty", nextGoal)
	}
}

func TestParseSelfSummary_CaseInsensitive(t *testing.T) {
	input := `<EVAL>uppercase eval</EVAL><Memory>mixed memory</Memory>`
	eval, memory, nextGoal := ParseSelfSummary(input)
	if eval != "uppercase eval" {
		t.Errorf("eval: got %q, want %q", eval, "uppercase eval")
	}
	if memory != "mixed memory" {
		t.Errorf("memory: got %q, want %q", memory, "mixed memory")
	}
	if nextGoal != "" {
		t.Errorf("next_goal: got %q, want empty", nextGoal)
	}
}

func TestParseSelfSummary_MultilineBody(t *testing.T) {
	input := "<eval>line one\nline two\nline three</eval>"
	eval, _, _ := ParseSelfSummary(input)
	if eval != "line one\nline two\nline three" {
		t.Errorf("eval: got %q, want multiline body", eval)
	}
}

func TestParseSelfSummary_MultipleOccurrencesFirstWins(t *testing.T) {
	input := `<eval>first eval</eval> some text <eval>second eval</eval>`
	eval, _, _ := ParseSelfSummary(input)
	if eval != "first eval" {
		t.Errorf("eval: got %q, want %q", eval, "first eval")
	}
}

func TestParseSelfSummary_EmptyBody(t *testing.T) {
	eval, memory, nextGoal := ParseSelfSummary("<eval></eval>")
	if eval != "" {
		t.Errorf("eval: got %q, want empty string", eval)
	}
	if memory != "" {
		t.Errorf("memory: got %q, want empty", memory)
	}
	if nextGoal != "" {
		t.Errorf("next_goal: got %q, want empty", nextGoal)
	}
}
