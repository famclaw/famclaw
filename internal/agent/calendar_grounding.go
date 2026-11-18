package agent

import (
	"strings"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/store"
)

// calendarRequestKind is the typed intent for a user message in the
// personal-calendar domain.
type calendarRequestKind int

const (
	calendarNone           calendarRequestKind = iota // not personal-calendar domain
	calendarPublicResearch                            // public API/docs/integration research
	calendarDiscovery                                 // "which calendars can you access?"
	calendarEventRead                                 // "what is on my calendar this week?"
	calendarMutation                                  // "add a meeting to my calendar"
)

// classifyExplicitCalendarIntent returns the typed calendar kind for a single
// user message based solely on its own text, without context. It is the
// primitive used by the forward-fold resolver.
func classifyExplicitCalendarIntent(text string) calendarRequestKind {
	lower := strings.ToLower(text)
	// Mutation.
	if calendarMutationIntent(text) {
		return calendarMutation
	}
	// Discovery: "which calendars can you see/access"
	if calendarDiscoveryOnly(text) {
		return calendarDiscovery
	}
	// How-to / feature questions are public research, not personal data.
	// "How do I share my calendar?" asks about the feature, not events.
	// Checked BEFORE strong markers so "my calendar" in a how-to context
	// does not override the feature-question classification.
	if isHowToCalendarQuestion(text) {
		if publicCalendarResearch(text) {
			return calendarPublicResearch
		}
		return calendarNone
	}
	// Strong personal-calendar markers take priority over public-research so
	// a mixed request like "Check my private calendar and find public docs"
	// is classified as personal (the data request dominates).
	//
	// Principle: generic ownership fragments ("my calendar", "what do I have
	// on") require real calendar-data intent — a temporal anchor (day, week,
	// month, today, tomorrow) or an event/schedule noun. Purely technical
	// calendar-app questions (crashing, troubleshooting, documentation) are
	// caught earlier by the how-to check or fall through to public research.
	strongMarkers := []string{
		"my schedule", "my events", "my appointments",
		"my private calendar", "check my calendar", "look at my calendar",
		"how's my week", "how is my week", "hows my week",
		"show my calendar", "show my schedule",
		"list my events", "list my appointments",
	}
	// "my calendar" is only personal when paired with a data-intent anchor
	// (temporal word or event/schedule noun), not when it refers to the app.
	if strings.Contains(lower, "my calendar") && calendarDataIntent(lower) {
		return calendarEventRead
	}
	for _, mk := range strongMarkers {
		if strings.Contains(lower, mk) {
			return calendarEventRead
		}
	}
	// "What do I have on" requires a temporal or calendar-data anchor.
	if strings.Contains(lower, "what do i have on") && calendarDataIntent(lower) {
		return calendarEventRead
	}
	// "my calendar" variants with different word order.
	if strings.Contains(lower, "calendar") && (strings.Contains(lower, "my") || strings.Contains(lower, "mine")) && calendarDataIntent(lower) {
		return calendarEventRead
	}
	// "Show me all events" is only personal when anchored to a calendar context.
	if strings.Contains(lower, "show me all events") && strings.Contains(lower, "calendar") {
		return calendarEventRead
	}
	// Named-calendar event questions: "What is on the Family calendar?"
	if strings.Contains(lower, "calendar") && (strings.Contains(lower, "what is on") || strings.Contains(lower, "what's on") || strings.Contains(lower, "whats on")) {
		return calendarEventRead
	}
	// Public API/docs research — checked AFTER personal markers so that
	// "Find Google Calendar API documentation for events.list" is public
	// (no personal markers), while "Check my private calendar and find
	// public docs" is personal (strong marker caught above).
	if publicCalendarResearch(text) {
		return calendarPublicResearch
	}
	// Event nouns attached to calendar even without "my": "Show me all events
	// from calendar you have access to".
	if strings.Contains(lower, "calendar") && (strings.Contains(lower, "events") || strings.Contains(lower, "appointments") || strings.Contains(lower, "meetings")) {
		return calendarEventRead
	}
	return calendarNone
}

// resolveCalendarRequestKind determines the typed calendar domain for the
// current user message using a bounded forward fold over USER messages.
//
// Forward-fold rules:
//  1. Each user message's explicit intent is classified first
//     (public/discovery/event-read/mutation).
//  2. A correction, retry, or named follow-up ("How about Family?")
//     PRESERVES the current domain.
//  3. A contents-ellipsis ("What in it?") TRANSITIONS discovery to
//     event-read; in all other domains it preserves.
//  4. Unrelated input (no calendar markers, not a follow-up) RESETS to none.
//  5. A mixed message with both personal-data and public-docs markers is
//     classified as personal (data request dominates).
//
// The current saved user row is processed exactly once. Assistant messages
// are never used as evidence.
func resolveCalendarRequestKind(userMessage string, history []*store.Message) calendarRequestKind {
	// Forward fold: track the current domain as we walk user messages
	// from oldest to current.
	var domain calendarRequestKind = calendarNone
	for _, msg := range history {
		if msg.Role != "user" {
			continue
		}
		domain = foldUserMessage(domain, msg.Content)
	}
	// The current user message may not yet be in history (it is saved
	// before the agent loop starts, but some call sites pass history
	// without it). Process it exactly once: if the last history row is
	// the current message, the fold already consumed it; otherwise apply
	// the current message on top of the folded domain.
	if len(history) > 0 && history[len(history)-1].Role == "user" &&
		history[len(history)-1].Content == userMessage {
		return domain
	}
	return foldUserMessage(domain, userMessage)
}

// foldUserMessage advances the domain state machine with one user message.
func foldUserMessage(current calendarRequestKind, text string) calendarRequestKind {
	// 1. Explicit intent classification.
	explicit := classifyExplicitCalendarIntent(text)
	if explicit != calendarNone {
		return explicit
	}
	// 2. No explicit calendar intent in this message.
	//    Correction / retry / named-followup: preserve current domain.
	if current != calendarNone &&
		(isRetryIntent(text) || isCorrectionIntent(text) || isEllipticalFollowUp(text)) {
		// 3. Contents-ellipsis transitions discovery → event-read.
		if current == calendarDiscovery && isContentsEllipsis(text) {
			return calendarEventRead
		}
		return current
	}
	// 4. Unrelated input: reset.
	return calendarNone
}

// isContentsEllipsis reports whether the message specifically asks about the
// CONTENTS of something previously enumerated (calendars, lists), as opposed
// to asking about the inventory itself.
func isContentsEllipsis(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"what in it", "whats in it", "what's in it",
		"what in them", "what's in them",
		"what's on it", "whats on it",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// isHowToCalendarQuestion reports whether the text is a how-to / instruction
// question about calendar features (sharing, setup, configuration) rather
// than a request for personal event data.
func isHowToCalendarQuestion(text string) bool {
	lower := strings.ToLower(text)
	howTo := strings.Contains(lower, "how do i") || strings.Contains(lower, "how to") ||
		strings.Contains(lower, "how does") || strings.Contains(lower, "how can i")
	feature := strings.Contains(lower, "share") || strings.Contains(lower, "set up") ||
		strings.Contains(lower, "configure") || strings.Contains(lower, "enable") ||
		strings.Contains(lower, "link") || strings.Contains(lower, "sync") ||
		strings.Contains(lower, "permission") || strings.Contains(lower, "invit")
	return howTo && feature
}

// calendarMutationIntent reports whether the user asks to create, modify, or
// delete calendar events.
func calendarMutationIntent(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"add a", "add my", "create a", "create my", "schedule a", "schedule my",
		"remove a", "remove my", "delete a", "delete my", "cancel a", "cancel my",
		"move a", "move my", "reschedule",
		"new event", "new appointment", "new meeting",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) && (strings.Contains(lower, "calendar") || strings.Contains(lower, "event") || strings.Contains(lower, "appointment") || strings.Contains(lower, "meeting")) {
			return true
		}
	}
	return false
}

// isEllipticalFollowUp reports whether the user message is a short elliptical
// follow-up that inherits domain from the prior user request, e.g. "How about
// Family?" after "Which calendars can you access?" or "What in it?" after
// calendar enumeration.
func isEllipticalFollowUp(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"how about", "what about", "and family", "and main",
		"just family", "just main",
		"what in it", "whats in it", "what's in it",
		"what's on it", "whats on it",
		"what in them", "what's in them",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// calendarDiscoveryOnly reports whether the user message is purely about
// enumerating available calendars, not about their events. Requires a
// personal-access anchor so general product questions ("What calendars
// support CalDAV?") are not treated as personal inventory.
func calendarDiscoveryOnly(text string) bool {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "calendar") {
		return false
	}
	discoveryMarkers := []string{
		"which calendars can you", "what calendars can you",
		"list calendars", "calendars can you see",
		"calendars can you access", "calendars do you have",
		"calendars are available", "calendars do you have access",
		"which calendars are", "what calendars are",
		"calendar access",
	}
	for _, mk := range discoveryMarkers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// calendarDataIntent reports whether the text contains a temporal anchor
// (day, week, month, today, tomorrow, yesterday, this, next) or a calendar-data
// noun (event, appointment, meeting, schedule) that indicates the user is
// asking about their own schedule data rather than the calendar application
// itself.
func calendarDataIntent(lower string) bool {
	temporal := []string{
		"today", "tomorrow", "yesterday", "this week", "this month",
		"next week", "next month", "this weekend", "this morning",
		"this afternoon", "this evening", "tonight",
		"monday", "tuesday", "wednesday", "thursday", "friday",
		"saturday", "sunday", "day", "week", "month",
	}
	for _, mk := range temporal {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	dataNouns := []string{
		"event", "appointment", "meeting", "schedule",
	}
	for _, mk := range dataNouns {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// publicCalendarResearch reports whether the user is asking about the Google
// Calendar API, calendar SDKs, sharing instructions, or public documentation —
// NOT about their own personal events.
func publicCalendarResearch(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"google calendar api", "calendar api", "calendar sdk",
		"calendar documentation", "calendar docs",
		"gcal api", "google calendar integration",
		"calendar oauth", "calendar service account",
		"how to build a calendar", "calendar app",
		"find public documentation", "public documentation",
		"authentication documentation", "api authentication",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	// "How do I share my calendar" — a how-to question about calendar features,
	// not a personal event data request.
	if (strings.Contains(lower, "how do i") || strings.Contains(lower, "how to") || strings.Contains(lower, "how does")) &&
		(strings.Contains(lower, "share my calendar") || strings.Contains(lower, "share calendar") ||
			strings.Contains(lower, "set up calendar") || strings.Contains(lower, "configure calendar") ||
			strings.Contains(lower, "enable calendar") || strings.Contains(lower, "link calendar")) {
		return true
	}
	return false
}

// isPersonalCalendarRequest reports whether the resolved kind is any personal
// calendar domain (discovery, event, mutation). Used to gate stale-search
// injection.
func isPersonalCalendarRequest(userMessage string, history []*store.Message) bool {
	switch resolveCalendarRequestKind(userMessage, history) {
	case calendarDiscovery, calendarEventRead, calendarMutation:
		return true
	}
	return false
}

// isCalendarEventRequest reports whether the resolved kind is an event read.
// Triggers the missing-event-read guard.
func isCalendarEventRequest(userMessage string, history []*store.Message) bool {
	return resolveCalendarRequestKind(userMessage, history) == calendarEventRead
}

// isCalendarDiscoveryRequest reports whether the resolved kind is discovery.
// Triggers the missing-discovery-evidence guard.
func isCalendarDiscoveryRequest(userMessage string, history []*store.Message) bool {
	return resolveCalendarRequestKind(userMessage, history) == calendarDiscovery
}

// calendarDiscoveryTools are tool names that provide calendar inventory.
func isCalendarDiscoveryTool(name string) bool {
	switch name {
	case "list-calendars", "get-calendars", "search-calendars":
		return true
	}
	if strings.HasPrefix(name, "mcp__") {
		rest := name[len("mcp__"):]
		if i := strings.Index(rest, "__"); i >= 0 {
			return isCalendarDiscoveryTool(rest[i+2:])
		}
	}
	return false
}

// calendarEventTools are tool names that provide calendar event data.
func isCalendarEventTool(name string) bool {
	switch name {
	case "list-events", "search-events", "get-events":
		return true
	}
	if strings.HasPrefix(name, "mcp__") {
		rest := name[len("mcp__"):]
		if i := strings.Index(rest, "__"); i >= 0 {
			return isCalendarEventTool(rest[i+2:])
		}
	}
	return false
}

// recordedCalendarEvidence reports whether a successful tool call of the given
// kind was recorded this turn. Only DispositionAttempted with OutcomeSuccess
// counts as read evidence; denied/pending/skipped/unknown are not.
func recordedCalendarEvidence(results []agentcore.ToolResult, kind calendarRequestKind) bool {
	toolCheck := isCalendarEventTool
	if kind == calendarDiscovery {
		toolCheck = isCalendarDiscoveryTool
	}
	for _, tr := range results {
		if tr.Error != nil {
			continue
		}
		if tr.Disposition != agentcore.DispositionAttempted {
			continue
		}
		if tr.Outcome != agentcore.OutcomeSuccess {
			continue
		}
		if toolCheck(tr.ToolName) {
			return true
		}
	}
	return false
}

// hasCalendarEventTool reports whether any calendar event-read tool is in the
// turn's available tool list.
func hasCalendarEventTool(turn *agentcore.Turn) bool {
	for _, t := range turn.Tools {
		if isCalendarEventTool(t.Name) || isCalendarDiscoveryTool(t.Name) {
			return true
		}
	}
	return false
}

// isCorrectionIntent reports whether the user is asking to redo or verify a
// prior answer.
func isCorrectionIntent(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{
		"check again", "check it again", "try again", "look again",
		"that is wrong", "thats wrong", "that's wrong",
		"that's not right", "thats not right", "that is not right",
		"verify again", "double check",
	}
	for _, mk := range markers {
		if strings.Contains(lower, mk) {
			return true
		}
	}
	return false
}

// calendarToolInstruction is a short current-turn directive injected when
// calendar event tools are available and the user asks about their personal
// schedule.
const calendarToolInstruction = "Calendar event tools are available for this request. Answer personal schedule questions ONLY from event data retrieved this turn (list-events or search-events). Do not reuse claims from earlier turns without a fresh successful event read. If no event data is retrieved, say you cannot verify the schedule."

// calendarDiscoveryToolInstruction is injected for calendar inventory/discovery
// requests (which calendars are available), scoping the answer to the current
// list-calendars result rather than event data.
const calendarDiscoveryToolInstruction = "Calendar discovery tools are available for this request. Answer which calendars are available ONLY from the current list-calendars result this turn. Do not reuse calendar names from earlier turns without a fresh successful lookup. If no calendar list is retrieved, say you cannot verify which calendars are available."

// calendarUnverifiedMsg is the honest inability-to-verify message delivered
// when the model claims personal calendar events without a successful event
// read this turn.
const calendarUnverifiedMsg = "I can't verify your schedule — I was unable to retrieve your calendar events this time. Please try asking again."

// calendarDiscoveryUnverifiedMsg is delivered when the model claims calendar
// inventory without a successful discovery tool read this turn.
const calendarDiscoveryUnverifiedMsg = "I can't verify which calendars are available — I was unable to retrieve the calendar list this time. Please try asking again."

// kindName returns a short human-readable name for log messages.
func kindName(kind calendarRequestKind) string {
	switch kind {
	case calendarDiscovery:
		return "discovery"
	case calendarEventRead:
		return "event"
	case calendarMutation:
		return "mutation"
	case calendarPublicResearch:
		return "public-research"
	default:
		return "none"
	}
}
