package wasmskill

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// JSON-RPC 2.0 method names on the skill endpoint.
const (
	MethodInit    = "skill_init"
	MethodInvoke  = "skill_invoke"
	MethodCleanup = "skill_cleanup"
)

// Class names for guest-reported errors (see GuestError and SkillError).
const (
	ErrClassSandbox  = "sandbox"  // host sandbox violation; not retryable
	ErrClassLogic    = "logic"    // guest rejected the request; not retryable
	ErrClassInternal = "internal" // guest/host failure; may be retryable
)

// InitRequest is the "skill_init" params object.
type InitRequest struct {
	Meta InitMeta `json:"meta"`
}

// InitMeta identifies the host and the skill being initialised.
type InitMeta struct {
	Version   int    `json:"version"`
	Agent     string `json:"agent"`
	SkillName string `json:"skill_name"`
}

// InitResult is the "skill_init" result object.
type InitResult struct {
	Tools []ToolDef `json:"tools"`
}

// ToolDef is one tool a skill exposes.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is the JSON Schema the guest validates args against.
	InputSchema json.RawMessage `json:"input_schema"`
}

// InvokeRequest is the "skill_invoke" params object.
type InvokeRequest struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	Meta InvokeMeta     `json:"meta"`
}

// InvokeMeta carries per-call context the guest may use.
type InvokeMeta struct {
	CallID  string `json:"call_id"`
	User    string `json:"user,omitempty"`
	Context string `json:"user_context,omitempty"`
}

// Result is the "skill_invoke" result object.
type Result struct {
	OK      bool           `json:"ok"`
	Content ResultContent  `json:"content"`
	Data    map[string]any `json:"data"`
}

// ResultContent is the user-visible payload of a skill result.
type ResultContent struct {
	Text string `json:"text"`
}

// GuestError is the "data" object a guest may attach to an RPC error
// envelope to classify it for the host.
type GuestError struct {
	Class     string `json:"class"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

// rpcRequest is one host -> guest NDJSON line.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// guestErrorBody is the "error" object of a guest -> host line.
type guestErrorBody struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    *GuestError `json:"data"`
}

// rpcResponse is one guest -> host NDJSON line.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *guestErrorBody `json:"error"`
}

// encodeLine marshals v to one NDJSON line.
func encodeLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("wasmskill: encoding message: %w", err)
	}
	return append(b, '\n'), nil
}

// parseResponse decodes one guest NDJSON line into an rpcResponse,
// reporting non-JSON lines as errors.
func parseResponse(line []byte) (*rpcResponse, error) {
	trimmed := strings.TrimRight(string(line), "\r\n")
	if trimmed == "" {
		return nil, errors.New("wasmskill: empty guest line")
	}
	var r rpcResponse
	if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
		return nil, fmt.Errorf("wasmskill: guest line is not NDJSON: %w", err)
	}
	if r.JSONRPC != "2.0" {
		return nil, errors.New("wasmskill: guest line is not JSON-RPC 2.0")
	}
	return &r, nil
}
