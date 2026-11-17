package agentcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"io/fs"
	"log"
	"strings"
	"testing"
	"time"
)

func TestLaterQualifiedApprovalRequiresOrderedValidDecision(t *testing.T) {
	now := time.Now()
	legacy := map[string]any{"status": "denied", "decided_by": "parent", "decision_sequence": int64(4)}
	valid := map[string]any{"status": "approved", "decided_by": "parent-link", "decision_sequence": int64(5), "expires_at": now.Add(time.Hour)}
	tests := []struct {
		name   string
		change func(map[string]any, map[string]any)
		want   bool
	}{
		{"later", func(_, _ map[string]any) {}, true},
		{"reverse", func(l, q map[string]any) { l["decision_sequence"] = int64(6) }, false},
		{"tie", func(l, q map[string]any) { q["decision_sequence"] = int64(4) }, false},
		{"historical denial and new grant", func(l, q map[string]any) { l["decision_sequence"] = int64(0) }, true},
		{"two historical decisions", func(l, q map[string]any) { l["decision_sequence"] = int64(0); q["decision_sequence"] = int64(0) }, false},
		{"negative legacy", func(l, q map[string]any) { l["decision_sequence"] = int64(-1) }, false},
		{"pending", func(l, q map[string]any) { q["status"] = "pending" }, false},
		{"denied", func(l, q map[string]any) { q["status"] = "denied" }, false},
		{"expired", func(l, q map[string]any) { q["expires_at"] = now }, false},
		{"missing expiry", func(l, q map[string]any) { delete(q, "expires_at") }, false},
		{"malformed sequence", func(l, q map[string]any) { q["decision_sequence"] = "5" }, false},
		{"missing decision actor", func(l, q map[string]any) { q["decided_by"] = "" }, false},
		{"missing legacy actor", func(l, q map[string]any) { delete(l, "decided_by") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := map[string]any{}
			q := map[string]any{}
			for k, v := range legacy {
				l[k] = v
			}
			for k, v := range valid {
				q[k] = v
			}
			tt.change(l, q)
			if got := laterQualifiedApproval(q, l, now); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuiltinFailureClassificationInToolLoop(t *testing.T) {
	for _, tc := range []struct {
		name, class string
		cause       error
		readOnly    bool
	}{
		{"timeout", "Error class: timeout.", context.DeadlineExceeded, false},
		{"cancelled", "Error class: cancelled.", context.Canceled, true},
		{"not_found", "Error class: not found.", fs.ErrNotExist, false},
		{"permission", "Error class: permission denied.", fs.ErrPermission, true},
		{"unknown", "", errors.New("other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "fake-private-host-or-path"
			cause := fmt.Errorf("%s: %w", secret, tc.cause)
			chatter := &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}}
			calls := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__operation", ReadOnly: tc.readOnly}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{
				{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"x": "same"}}},
				{ID: "two", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"x": "same"}}},
			})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return secret, cause }})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(turn.ToolCalls) != 2 || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) {
				t.Fatalf("retry/dispatch: calls=%d results=%+v", calls, turn.ToolCalls)
			}
			first := turn.ToolCalls[0]
			if first.Disposition != DispositionAttempted || first.Outcome != OutcomeFailure || !errors.Is(first.Error, tc.cause) {
				t.Fatalf("outcome = %+v", first)
			}
			if strings.Contains(first.Output, secret) || (tc.class != "" && !strings.Contains(first.Output, tc.class)) {
				t.Errorf("unsafe or missing class: %q", first.Output)
			}
			if tc.class == "" && first.Output != ToolErrorDisplayChangesMayHaveOccurred {
				t.Errorf("unknown class = %q", first.Output)
			}
			if !tc.readOnly && !strings.Contains(first.Output, "some changes may still have occurred") {
				t.Errorf("write uncertainty lost: %q", first.Output)
			}
			if tc.readOnly && !strings.Contains(first.Output, "effects are not verified") {
				t.Errorf("read-only caveat lost: %q", first.Output)
			}
			var sawClass bool
			for _, msg := range chatter.messages {
				if strings.Contains(msg.Content, secret) {
					t.Fatal("private detail leaked to model")
				}
				if tc.class != "" && strings.Contains(msg.Content, tc.class) {
					sawClass = true
				}
			}
			if tc.class != "" && !sawClass {
				t.Fatal("classified tool result did not reach model")
			}
		})
	}
}

func TestBuiltinSearchSafeMarkerPrecedesErrorClass(t *testing.T) {
	chatter := &bareNameMockChatter{responses: []llm.Message{{Content: "done"}}}
	turn := &Turn{User: &config.UserConfig{Name: "tester"}, Tools: []Tool{{Name: "builtin__web_search", ReadOnly: true}}}
	turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__web_search"}}})
	turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
	stage := NewStageToolLoop(ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
		return WebSearchMarkerRateLimited, context.DeadlineExceeded
	}})
	if err := stage(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Output != WebSearchMarkerRateLimited {
		t.Fatalf("search marker changed: %+v", turn.ToolCalls)
	}
}

func TestOutcomeBuiltinSinks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure bool
		rebuild bool
	}{
		{"failure_legacy", true, false}, {"failure_rebuilt", true, true}, {"success", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "fake-*" fixture: synthetic test marker, not a live secret. The
			// check-private-details guard (CREDENTIAL shape) only exempts the
			// documented placeholder conventions (fake-*/test-*/changeme/...),
			// so keep this value inside that allowlist.
			const secret = "fake-private-marker"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			chatter := &leakChatter{}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "test-id", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"token": secret}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			deps := ToolLoopDeps{RebuildHistory: tc.rebuild, MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				if tc.failure {
					return "", errors.New(secret)
				}
				return "safe-success", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing result")
			}
			if tc.failure {
				if turn.ToolCalls[0].Error == nil {
					t.Error("failure recorded as success")
				}
				if strings.Contains(logs.String(), secret) {
					t.Error("private cause leaked to logs")
				}
				for _, call := range chatter.calls {
					for _, msg := range call.msgs {
						if strings.Contains(msg.Content, secret) {
							t.Error("private cause leaked to model")
						}
					}
				}
			} else if turn.ToolCalls[0].Error != nil || turn.ToolCalls[0].Output != "safe-success" {
				t.Error("success not preserved")
			}
		})
	}
}

func TestOutcomeCapPendingCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  bool
	}{{"cap", true}, {"stopped", false}} {
		t.Run(tc.name, func(t *testing.T) {
			call := llm.ToolCall{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{}}}
			reply := llm.Message{Content: "Everything is complete."}
			if tc.cap {
				reply.ToolCalls = []llm.ToolCall{call}
			}
			count := 0
			execs := 0
			chatter := &mockChatter{responses: []llm.Message{reply}, callCount: &count}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{call})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { execs++; return "ok", nil }}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if execs != 1 {
				t.Fatalf("got %d executions; want 1", execs)
			}
			if tc.cap && (!strings.Contains(strings.ToLower(turn.Output), "not run") || !strings.Contains(strings.ToLower(turn.Output), "limit")) {
				t.Error("cap response failed to disclose unexecuted request")
			}
			if tc.cap && !strings.Contains(strings.ToLower(turn.Output), "everything is complete.") {
				t.Errorf("cap hit must preserve the model's last response text, got %q", turn.Output)
			}
			if !tc.cap && turn.Output != "Everything is complete." {
				t.Error("stopped success changed")
			}
		})
	}
}

func TestOutcomeDispositionFinalAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, action, want string
		called             bool
	}{{"denied", "block", "denied", false}, {"pending", "request_approval", "pending", false}, {"success", "allow", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			eval := &approvalToolEval{decisions: map[string]policy.ToolDecision{"file_write": {Action: tc.action, Allow: tc.called}}}
			deps, called := makeApprovalDeps(t, eval, &fakeApprovalStore{})
			turn := makeApprovalTurn("child", "child", "file_write")
			if err := NewStageToolLoop(*deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if *called != tc.called {
				t.Error("wrong execution disposition")
			}
			if tc.want != "" && !strings.Contains(strings.ToLower(turn.Output), tc.want) {
				t.Errorf("final answer omitted %s status", tc.want)
			}
			if tc.want != "" && strings.Contains(strings.ToLower(turn.Output), "tool reported a failure") {
				t.Error("not-run call claimed as execution failure")
			}
			if tc.called && turn.Output != "done" {
				t.Error("success answer changed")
			}
		})
	}
}

func TestOutcomeMixedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{"mixed", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []llm.ToolCall{
				{ID: "one", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"index": "one"}}},
				{ID: "two", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"index": "two"}}},
			}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__operation"}}}
			turn.SetMeta("pending_tool_calls", calls)
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			count := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter {
				return &mockChatter{responses: []llm.Message{{Content: "Everything completed."}}, callCount: &count}
			}, BuiltinHandler: func(_ context.Context, _ string, args map[string]any) (string, error) {
				if tc.failed && args["index"] == "two" {
					return "", errors.New("partial operation")
				}
				return "ok", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 2 {
				t.Fatal("expected two tool calls")
			}
			if tc.failed && !strings.Contains(strings.ToLower(turn.Output), "fail") {
				t.Error("mixed execution lacks failure warning")
			}
			if !tc.failed && turn.Output != "Everything completed." {
				t.Error("success response changed")
			}
		})
	}
}

func TestOutcomePartialEffects(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"partial", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			effects := 0
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: "builtin__write"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call1", Function: llm.ToolCallFunction{Name: "builtin__write", Arguments: map[string]any{}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "write"}})
			count := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter {
				return &mockChatter{responses: []llm.Message{{Content: "Done."}}, callCount: &count}
			}, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				effects++
				if tc.fail {
					return "", errors.New("partial failure")
				}
				return "ok", nil
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if effects != 1 {
				t.Fatalf("effects=%d; want 1", effects)
			}
			if tc.fail && strings.Contains(turn.Output, "No action was completed") {
				t.Error("partial effect incorrectly described as no action completed")
			}
			if !tc.fail && turn.Output != "Done." {
				t.Errorf("success output = %q", turn.Output)
			}
		})
	}
}

func TestReadOnlyOutcomeDoesNotAssertUnverifiedEffects(t *testing.T) {
	registry := []Tool{{Name: "write"}, {Name: "lookup", ReadOnly: true}}
	for _, tc := range []struct {
		name  string
		calls []ToolResult
		want  []string
	}{
		{
			name: "successful mutation and uncertain lookup",
			calls: []ToolResult{
				{ToolName: "write", Disposition: DispositionAttempted, Outcome: OutcomeSuccess},
				{ToolName: "lookup", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
			},
			want: []string{"succeeded", "marked read-only", "uncertain"},
		},
		{
			name: "both mutations and lookup fail",
			calls: []ToolResult{
				{ToolName: "write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
				{ToolName: "lookup", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			want: []string{"changes may", "marked read-only", "failure"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ToLower(outcomeWarning(tc.calls, registry))
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("warning %q lacks %q", got, want)
				}
			}
			if strings.Contains(got, "no changes") {
				t.Errorf("warning asserts unverified absence of changes: %q", got)
			}
		})
	}
}

// TestOutcomeWarning_BothFailureCategories is the regression test for the
// co-occurring attempted-category gap: a turn containing both a mutating
// tool failure and a read-only tool failure must mention BOTH categories
// (previously the else-if chain emitted only the mutation note and silently
// dropped the read-only one), with the more important mutation note first.
func TestOutcomeWarning_BothFailureCategories(t *testing.T) {
	registry := []Tool{
		{Name: "builtin__web_search", ReadOnly: true},
		{Name: "builtin__file_write"},
	}
	calls := []ToolResult{
		{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
		{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
	}
	s := strings.ToLower(outcomeWarning(calls, registry))
	const mutNote = "some changes may still have occurred"
	const roNote = "marked read-only"
	if iMut, iRO := strings.Index(s, mutNote), strings.Index(s, roNote); iMut < 0 || iRO < 0 {
		t.Fatalf("warning must contain both the mutation note (%q) and the read-only note (%q), got: %q", mutNote, roNote, s)
	}
	if iMut, iRO := strings.Index(s, mutNote), strings.Index(s, roNote); iMut > iRO {
		t.Errorf("mutation note must come before the read-only note, got: %q", s)
	}
	// Same assertion with a co-occurring success in the turn: both notes are
	// re-worded for the success case, so check the success-qualified forms.
	calls = append([]ToolResult{{ToolName: "builtin__web_fetch", Disposition: DispositionAttempted, Outcome: OutcomeSuccess}}, calls...)
	s = strings.ToLower(outcomeWarning(calls, registry))
	const mutNoteSuccess = "another reported a failure; some changes may have occurred"
	const roNoteSuccess = "a tool marked read-only reported a failure"
	if iMut, iRO := strings.Index(s, mutNoteSuccess), strings.Index(s, roNoteSuccess); iMut < 0 || iRO < 0 {
		t.Errorf("with a success present the warning must still contain both notes, got: %q", s)
	}
}

func TestOutcomeWarningStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  []ToolResult
		tools  []Tool
		want   []string
		absent []string
	}{
		{"all success", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}}, nil, nil, []string{"fail", "uncertain"}},
		{"success and failure", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}}, nil, []string{"succeeded", "failure"}, nil},
		{"failure and unknown", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, nil, []string{"failure", "uncertain"}, nil},
		{"all three", []ToolResult{{Disposition: DispositionAttempted, Outcome: OutcomeSuccess}, {Disposition: DispositionAttempted, Outcome: OutcomeFailure}, {Disposition: DispositionAttempted, Outcome: OutcomeUnknown}}, nil, []string{"succeeded", "failure", "uncertain"}, nil},
		{"policy and dedup", []ToolResult{{Disposition: DispositionDenied}, {Disposition: DispositionPending}, {Disposition: DispositionSkipped}}, nil, []string{"denied", "pending", "skipped"}, []string{"failure", "succeeded"}},
		// Read-only scoping: a rate-limited web_search (or any tool declared
		// ReadOnly in the registry) must not emit the "changes may have
		// occurred" mutation warning.
		{
			"read-only failure",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"failure", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"read-only failure via bare name",
			[]ToolResult{{ToolName: "web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"failure", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"unmarked failure stays conservative",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__web_search"}},
			[]string{"failure", "changes may still have occurred"},
			[]string{"no changes were made"},
		},
		{
			"mutation failure still warns",
			[]ToolResult{{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure}},
			[]Tool{{Name: "builtin__file_write"}},
			[]string{"failure", "changes may still have occurred"},
			[]string{"no changes were made"},
		},
		{
			"mixed read-only and mutation failure",
			[]ToolResult{
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
				{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}, {Name: "builtin__file_write"}},
			[]string{"failure", "changes may still have occurred", "marked read-only"},
			nil,
		},
		{
			"mixed mutation and read-only unknown",
			[]ToolResult{
				{ToolName: "builtin__file_write", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeUnknown},
			},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}, {Name: "builtin__file_write"}},
			[]string{"changes may have occurred", "marked read-only"},
			nil,
		},
		{
			"read-only failure with success",
			[]ToolResult{
				{ToolName: "builtin__web_fetch", Disposition: DispositionAttempted, Outcome: OutcomeSuccess},
				{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeFailure},
			},
			[]Tool{{Name: "builtin__web_fetch", ReadOnly: true}, {Name: "builtin__web_search", ReadOnly: true}},
			[]string{"succeeded", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"read-only unknown",
			[]ToolResult{{ToolName: "builtin__web_search", Disposition: DispositionAttempted, Outcome: OutcomeUnknown}},
			[]Tool{{Name: "builtin__web_search", ReadOnly: true}},
			[]string{"uncertain", "marked read-only"},
			[]string{"changes may"},
		},
		{
			"mutation unknown still warns",
			[]ToolResult{{ToolName: "mcp__android__tap", Disposition: DispositionAttempted, Outcome: OutcomeUnknown}},
			[]Tool{{Name: "mcp__android__tap"}},
			[]string{"uncertain", "changes may"},
			[]string{"no changes were made"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.ToLower(outcomeWarning(tc.calls, tc.tools))
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("warning lacks %q", w)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(s, w) {
					t.Errorf("warning incorrectly contains %q", w)
				}
			}
		})
	}
}

func TestUnavailableToolRetryAndHistoryRemainSkipped(t *testing.T) {
	for _, rebuild := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "rebuilt"}[rebuild], func(t *testing.T) {
			const name, secret = "mcp__missing", "fake-private-argument"
			args := map[string]any{"token": secret}
			chatter := &dedupChatter{responses: []llm.Message{
				{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "retry", Function: llm.ToolCallFunction{Name: name, Arguments: args}}}},
				{Role: "assistant", Content: "done"},
			}}
			turn := &Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []Tool{{Name: name}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "first", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			backendCalls := 0
			deps := ToolLoopDeps{MaxIterations: 3, RebuildHistory: rebuild, ClientFactory: func(*Turn) llm.Chatter { return chatter },
				BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { backendCalls++; return "", nil }}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if backendCalls != 0 || len(turn.ToolCalls) != 2 {
				t.Fatalf("executions=%d results=%d", backendCalls, len(turn.ToolCalls))
			}
			if turn.ToolCalls[0].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[0].Error, ErrUnknownTool) ||
				turn.ToolCalls[1].Disposition != DispositionSkipped || !errors.Is(turn.ToolCalls[1].Error, ErrToolAlreadyFailed) ||
				!errors.Is(turn.ToolCalls[1].Error, ErrUnknownTool) {
				t.Fatal("unavailable/duplicate error or skipped disposition was lost")
			}
			if len(chatter.captured) != 2 {
				t.Fatalf("model calls=%d", len(chatter.captured))
			}
			var text strings.Builder
			for _, message := range chatter.captured[1] {
				if rebuild || message.Role == "tool" && message.ToolCallID == "retry" {
					text.WriteString(message.Content)
				}
			}
			if !strings.Contains(text.String(), "tool unavailable; not run") || strings.Contains(text.String(), "blocked by policy") || strings.Contains(text.String(), secret) {
				t.Errorf("unavailable feedback missing or unsafe: %q", text.String())
			}
		})
	}
}
