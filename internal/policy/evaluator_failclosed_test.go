package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/storage"
)

// errStore is a storage.Store whose NewTransaction fails after the first call.
// The first call (during PrepareForEval) succeeds so the query can be prepared;
// the second call (during Eval) returns an error, exercising the fail-closed
// path in Evaluate.
type errStore struct {
	storage.PolicyNotSupported
	storage.TriggersNotSupported
	mu        sync.Mutex
	firstCall bool
	err       error
}

func (s *errStore) NewTransaction(context.Context, ...storage.TransactionParams) (storage.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstCall {
		return nil, s.err
	}
	s.firstCall = true
	return noopTxn{}, nil
}

func (s *errStore) Read(context.Context, storage.Transaction, storage.Path) (any, error) {
	return nil, &storage.Error{Code: storage.NotFoundErr}
}

func (s *errStore) Write(context.Context, storage.Transaction, storage.PatchOp, storage.Path, any) error {
	return nil
}

func (s *errStore) Commit(context.Context, storage.Transaction) error {
	return nil
}

func (s *errStore) Truncate(context.Context, storage.Transaction, storage.TransactionParams, storage.Iterator) error {
	return nil
}

func (s *errStore) Abort(context.Context, storage.Transaction) {}

type noopTxn struct{}

func (noopTxn) ID() uint64 { return 0 }

// TestEvaluate_ActionEvalErrorFailClosed verifies that when the OPA action
// query returns an error, Evaluate fails closed — returning a block decision
// and a wrapped error. This exercises the fail-closed branch added to protect
// against policy evaluation failures.
func TestEvaluate_ActionEvalErrorFailClosed(t *testing.T) {
	ev, err := NewEvaluator("", "", "")
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	// Prepare a query whose store fails NewTransaction on the second call:
	// first call during PrepareForEval succeeds, second call during Eval fails.
	brokenQ, err := rego.New(
		rego.Query("data.family.decision.action"),
		rego.Store(&errStore{err: errors.New("simulated store failure")}),
	).PrepareForEval(context.Background())
	if err != nil {
		t.Fatalf("PrepareForEval: %v", err)
	}
	ev.actionQuery = brokenQ

	d, err := ev.Evaluate(context.Background(),
		makeInput("parent", "", "general", "req-failclosed", nil))

	// The fail-closed path must return both an error and a block decision.
	if err == nil {
		t.Fatal("expected error when action query eval fails, got nil")
	}
	if d.Action != "block" {
		t.Errorf("fail-closed: Action = %q, want %q", d.Action, "block")
	}
	if d.Reason != "Policy evaluation error" {
		t.Errorf("fail-closed: Reason = %q, want %q", d.Reason, "Policy evaluation error")
	}
	if !strings.Contains(err.Error(), "evaluating action") {
		t.Errorf("fail-closed: error should wrap 'evaluating action', got %q", err.Error())
	}
}
