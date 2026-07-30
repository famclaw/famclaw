package policy

// Input is the data sent to the OPA policy evaluator.
type Input struct {
	User      UserInput      `json:"user"`
	Query     QueryInput     `json:"query"`
	RequestID string         `json:"request_id"`
	Approvals map[string]any `json:"approvals"`
}

// UserInput identifies the user making the request.
type UserInput struct {
	Role     string `json:"role"`      // parent | child
	AgeGroup string `json:"age_group"` // under_8 | age_8_12 | age_13_17
	Name     string `json:"name"`
}

// QueryInput describes the user's message.
type QueryInput struct {
	Category string `json:"category"`
	Text     string `json:"text"`
}

// Decision is the policy engine's verdict.
type Decision struct {
	Action string `json:"action"` // allow | block | request_approval | pending
	Reason string `json:"reason"`
}

// ToolCallInput is the payload sent to data.family.tool_policy when
// evaluating a tool call. The shape matches the existing tool_policy.rego
// rules, which read input.user.{role,age_group} and input.tool_name.
//
// Args carries the tool-call arguments so the policy can inspect content
// (e.g. detecting executable file_write payloads). RequestID and Approvals
// drive the per-call approval flow: the evaluator checks whether an approval
// already exists for this child+tool+args combination and what status it has.
type ToolCallInput struct {
	User      UserInput      `json:"user"`
	ToolName  string         `json:"tool_name"`
	Args      map[string]any `json:"args,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Approvals map[string]any `json:"approvals,omitempty"`
}

// ToolDecision is the result of a tool-call policy check.
// Action is the primary verdict: "allow" | "block" | "request_approval".
// Allow is derived from Action for backward compatibility with callers that
// only check the boolean (false for both block and request_approval).
// Reason is a human-readable explanation for blocked / approval-needed calls.
type ToolDecision struct {
	Allow  bool   `json:"allow"`
	Action string `json:"action"` // allow | block | request_approval
	Reason string `json:"reason"`
}

// OutputInput is the payload sent to data.family.output_policy.
// Field names match the Rego input shape exactly.
type OutputInput struct {
	User          UserInput `json:"user"`
	Gateway       string    `json:"gateway"`
	DraftResponse string    `json:"draft_response"`
}

// OutputDecision is the result of an output policy check.
type OutputDecision struct {
	Allow  bool     `json:"allow"`
	Reason string   `json:"reason"`
	Redact []string `json:"redact"`
}

// SkillPromptInput is the payload sent to data.family.skill_prompt_policy.
// Fields are top-level (not nested) per the task spec.
type SkillPromptInput struct {
	SkillName  string `json:"skill_name"`
	PromptBody string `json:"prompt_body"`
	UserRole   string `json:"user_role"`
}

// SkillPromptDecision is the result of a skill-prompt policy check.
type SkillPromptDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
}
