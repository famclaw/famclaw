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
