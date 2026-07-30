package agentcore

import (
	"context"
	"errors"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

// fakeApprovalStore records UpsertApproval calls and can simulate failures.
// AllApprovalsForOPA returns the accumulated approvals map.
type fakeApprovalStore struct {
	created   []*store.Approval
	upsertErr error
	approvals map[string]any
	readErr   error
}

func (f *fakeApprovalStore) UpsertApproval(a *store.Approval) (bool, error) {
	if f.upsertErr != nil {
		return false, f.upsertErr
	}
	if f.created == nil {
		f.created = []*store.Approval{}
	}
	f.created = append(f.created, a)
	if f.approvals == nil {
		f.approvals = map[string]any{}
	}
	f.approvals[a.ID] = map[string]any{"status": "pending"}
	return true, nil
}

func (f *fakeApprovalStore) AllApprovalsForOPA() (map[string]any, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.approvals, nil
}

// approvalToolEval returns decisions based on the tool name.
type approvalToolEval struct {
	decisions map[string]policy.ToolDecision
	called    []policy.ToolCallInput
}

func (f *approvalToolEval) EvaluateToolCall(_ context.Context, in policy.ToolCallInput) (policy.ToolDecision, error) {
	f.called = append(f.called, in)
	if d, ok := f.decisions[in.ToolName]; ok {
		return d, nil
	}
	return policy.ToolDecision{Allow: true, Action: "allow"}, nil
}

// makeApprovalDeps builds a ToolLoopDeps with the given eval, store, and
// a builtin handler that records whether it was called.
func makeApprovalDeps(t *testing.T, eval *approvalToolEval, as ApprovalStore) (*ToolLoopDeps, *bool) {
	t.Helper()
	builtinCalled := false
	deps := &ToolLoopDeps{
		MaxIterations:   1,
		PolicyEvaluator: eval,
		ApprovalStore:   as,
		BuiltinHandler: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			builtinCalled = true
			return "ok", nil
		},
		ClientFactory: func(*Turn) llm.Chatter {
			return &mockChatter{
				responses: []llm.Message{{Content: "done"}},
			}
		},
	}
	return deps, &builtinCalled
}

func makeApprovalTurn(role, ageGroup, toolName string) *Turn {
	t := &Turn{
		User: &config.UserConfig{Name: "kid", Role: role, AgeGroup: ageGroup},
		Tools: []Tool{
			{Name: "builtin__" + toolName, InputSchema: map[string]any{"type": "object"}},
		},
	}
	t.SetMeta("pending_tool_calls", []llm.ToolCall{{
		ID:       "call1",
		Function: llm.ToolCallFunction{Name: toolName, Arguments: map[string]any{"path": "test.txt", "content": "hello"}},
	}})
	t.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "test"}})
	return t
}

func TestToolLoop_ChildFileReadSucceeds(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_read": {Allow: true, Action: "allow", Reason: "Allowed."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_read")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error != nil {
		t.Errorf("file_read should have succeeded, got error: %v", turn.ToolCalls[0].Error)
	}
	if !*builtinCalled {
		t.Error("builtin handler should have been called for allowed file_read")
	}
}

func TestToolLoop_ChildFileWriteNonExecutableSucceeds(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: true, Action: "allow", Reason: "Allowed."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error != nil {
		t.Errorf("non-executable file_write should have succeeded, got error: %v", turn.ToolCalls[0].Error)
	}
	if !*builtinCalled {
		t.Error("builtin handler should have been called for allowed file_write")
	}
}

func TestToolLoop_ChildExecutableFileWriteRequestsApproval(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "request_approval", Reason: "This file looks executable and needs parent approval."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("executable file_write should have a non-nil error (approval requested)")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrApprovalRequested) {
		t.Errorf("error should be ErrApprovalRequested, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called for request_approval")
	}
	if len(store.created) != 1 {
		t.Fatalf("expected 1 approval created, got %d", len(store.created))
	}
	a := store.created[0]
	if a.UserName != "kid" {
		t.Errorf("approval user = %q, want %q", a.UserName, "kid")
	}
	if a.Category != "tool:file_write" {
		t.Errorf("approval category = %q, want %q", a.Category, "tool:file_write")
	}
}

func TestToolLoop_ApprovedExecutableFileWriteExecutes(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: true, Action: "allow", Reason: "Allowed."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error != nil {
		t.Errorf("approved file_write should have succeeded, got error: %v", turn.ToolCalls[0].Error)
	}
	if !*builtinCalled {
		t.Error("builtin handler should have been called for approved file_write")
	}
}

func TestToolLoop_DeniedExecutableFileWriteDoesNotExecute(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "block", Reason: "A parent denied this executable file write."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("denied file_write should have a non-nil error")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called for denied file_write")
	}
}

func TestToolLoop_ExecutableFileWriteStoreFailureFailsClosed(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "request_approval", Reason: "needs approval"},
		},
	}
	store := &fakeApprovalStore{upsertErr: errors.New("db locked")}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("store failure should result in the tool not executing")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked (fail closed), got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called when approval creation failed")
	}
}

func TestToolLoop_ExecutableFileWriteNoStoreFailsClosed(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "request_approval", Reason: "needs approval"},
		},
	}
	deps, builtinCalled := makeApprovalDeps(t, eval, nil)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("nil store should fail closed — tool must not execute")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called when no approval store is available")
	}
}

func TestToolLoop_ChildSpawnAgentHardBlocked(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"spawn_agent": {Allow: false, Action: "block", Reason: "This tool is not available for your age group."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_13_17", "spawn_agent")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("spawn_agent should be blocked for children")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called for blocked spawn_agent")
	}
}

func TestToolLoop_AdminToolHardBlockedForChild(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"list_users": {Allow: false, Action: "block", Reason: "This tool is restricted to parents only."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "list_users")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("admin tool should be blocked for children")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called for blocked admin tool")
	}
}

func TestToolLoop_ParentUnaffected(t *testing.T) {
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: true, Action: "allow", Reason: "Allowed."},
		},
	}
	store := &fakeApprovalStore{}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("parent", "", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error != nil {
		t.Errorf("parent file_write should succeed, got error: %v", turn.ToolCalls[0].Error)
	}
	if !*builtinCalled {
		t.Error("builtin handler should have been called for parent file_write")
	}
}

func TestToolLoop_PendingApprovalDoesNotExecute(t *testing.T) {
	existingApprovals := map[string]any{
		"pending-req-id": map[string]any{"status": "pending"},
	}
	store := &fakeApprovalStore{approvals: existingApprovals}
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "pending", Reason: "Waiting for parent to decide"},
		},
	}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrApprovalPending) {
		t.Errorf("error should be ErrApprovalPending, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called for pending approval")
	}
}

func TestToolLoop_PendingApprovalDoesNotReCreateApproval(t *testing.T) {
	// When the evaluation returns "pending", no new approval should be
	// created — one already exists for this request.
	existingApprovals := map[string]any{
		"some-id": map[string]any{"status": "pending"},
	}
	store := &fakeApprovalStore{approvals: existingApprovals}
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "pending", Reason: "Waiting"},
		},
	}
	deps, _ := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(store.created) != 0 {
		t.Errorf("pending action should not create a new approval, got %d created", len(store.created))
	}
}

func TestToolLoop_ApprovalStoreReadFailureFailsClosed(t *testing.T) {
	// If AllApprovalsForOPA fails, the policy cannot see a prior denial.
	// Proceeding would let a denied executable file_write look like a
	// fresh request — failing open. The tool must be blocked.
	eval := &approvalToolEval{
		decisions: map[string]policy.ToolDecision{
			"file_write": {Allow: false, Action: "request_approval", Reason: "needs approval"},
		},
	}
	store := &fakeApprovalStore{readErr: errors.New("db read failed")}
	deps, builtinCalled := makeApprovalDeps(t, eval, store)
	turn := makeApprovalTurn("child", "age_8_12", "file_write")

	if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
		t.Fatalf("stage error: %v", err)
	}

	if len(turn.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].Error == nil {
		t.Error("approval store read failure should block the tool")
	}
	if !errors.Is(turn.ToolCalls[0].Error, ErrToolBlocked) {
		t.Errorf("error should be ErrToolBlocked, got: %v", turn.ToolCalls[0].Error)
	}
	if *builtinCalled {
		t.Error("builtin handler should NOT have been called when approval store read fails")
	}
	if len(store.created) != 0 {
		t.Errorf("no approval should be created when store read fails, got %d", len(store.created))
	}
}
