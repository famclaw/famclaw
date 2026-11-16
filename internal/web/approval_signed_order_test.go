package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/notify"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

type observingToolPolicy struct {
	evaluator *policy.Evaluator
	status    string
}

func (o *observingToolPolicy) EvaluateToolCall(ctx context.Context, input policy.ToolCallInput) (policy.ToolDecision, error) {
	approval, _ := input.Approvals[input.RequestID].(map[string]any)
	o.status, _ = approval["status"].(string)
	return o.evaluator.EvaluateToolCall(ctx, input)
}

type localReapprovalChat struct{}

func (localReapprovalChat) Chat(context.Context, []llm.Message, float64, int, func(string)) (string, error) {
	return "done", nil
}
func (localReapprovalChat) ChatMessage(context.Context, []llm.Message, float64, int) (*llm.Message, error) {
	return &llm.Message{Content: "done"}, nil
}
func (localReapprovalChat) ChatWithTools(context.Context, []llm.Message, float64, int, []llm.ToolDef) (*llm.Message, error) {
	return &llm.Message{Content: "done"}, nil
}
func (localReapprovalChat) ChatSync(context.Context, []llm.Message, float64, int) (string, error) {
	return "done", nil
}

func legacyApprovalTestID(user, tool string, args map[string]any) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256([]byte(user + ":" + tool + ":" + string(encoded) + ":" + time.Now().UTC().Format("2006-01-02")))
	return hex.EncodeToString(h[:8])
}

func TestSignedLegacyDenyThenQualifiedApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evaluator, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Secret: "test-only-secret"}, Approval: config.ApprovalConfig{ExpiryHours: 24}}
	server := NewServer(ctx, cfg, "", db, nil, nil, nil, evaluator, nil, nil, nil, nil, nil)
	defer server.bgWG.Wait()
	args := map[string]any{"path": "test.sh", "content": "#!/bin/sh\necho hi"}
	legacyID := legacyApprovalTestID("kid", "file_write", args)
	v2ID := "v2-" + legacyApprovalTestID("kid", "builtin__file_write", args)
	if legacyID == v2ID {
		t.Fatal("distinct identities expected")
	}
	if _, err := db.UpsertApproval(ctx, &store.Approval{ID: legacyID, UserName: "kid", Category: "tool:file_write"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	observed := &observingToolPolicy{evaluator: evaluator}
	deps := agentcore.ToolLoopDeps{PolicyEvaluator: observed, ApprovalStore: db, MaxIterations: 1,
		ClientFactory:  func(*agentcore.Turn) llm.Chatter { return localReapprovalChat{} },
		BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil },
	}
	turn := func(tool string) *agentcore.Turn {
		v := &agentcore.Turn{User: &config.UserConfig{Name: "kid", Role: "child", AgeGroup: "age_8_12"},
			Tools: []agentcore.Tool{{Name: "builtin__file_write", Source: "builtin"}, {Name: "file_write", Source: "mcp"}}}
		v.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call", Function: llm.ToolCallFunction{Name: tool, Arguments: args}}})
		v.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
		if err := agentcore.NewStageToolLoop(deps)(ctx, v); err != nil {
			t.Fatal(err)
		}
		if len(v.ToolCalls) != 1 {
			t.Fatalf("expected one tool result, got %+v", v.ToolCalls)
		}
		return v
	}
	first := turn("builtin__file_write")
	if !errors.Is(first.ToolCalls[0].Error, agentcore.ErrApprovalRequested) || calls != 0 {
		t.Fatalf("v2 not requested: %+v calls=%d", first.ToolCalls, calls)
	}
	pending, err := db.GetApproval(ctx, v2ID)
	if err != nil || pending == nil || pending.Status != "pending" {
		t.Fatalf("stage did not create v2 pending: %+v %v", pending, err)
	}
	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/decide?token=invalid", nil))
	if invalid.Code != http.StatusForbidden {
		t.Fatalf("invalid signed link status=%d, want forbidden", invalid.Code)
	}
	decide := func(id, action string) {
		t.Helper()
		token := notify.GenerateToken(id, action, cfg.Server.Secret)
		req := httptest.NewRequest(http.MethodGet, "/decide?token="+url.QueryEscape(token), nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("signed %s decision status=%d", action, rec.Code)
		}
		server.bgWG.Wait()
	}
	decide(legacyID, "deny")
	blocked := turn("builtin__file_write")
	if !errors.Is(blocked.ToolCalls[0].Error, agentcore.ErrToolBlocked) || blocked.ToolCalls[0].Disposition != agentcore.DispositionDenied || calls != 0 {
		t.Fatalf("pending qualified request must not override legacy denial: %+v calls=%d", blocked.ToolCalls, calls)
	}
	if observed.status != "denied" {
		t.Fatalf("policy did not receive legacy denial: status=%q", observed.status)
	}
	stillPending, err := db.GetApproval(ctx, v2ID)
	if err != nil || stillPending == nil || !reflect.DeepEqual(stillPending, pending) {
		t.Fatalf("qualified pending approval changed: before=%+v after=%+v err=%v", pending, stillPending, err)
	}
	decide(v2ID, "approve")
	legacy, err := db.GetApproval(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := db.GetApproval(ctx, v2ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signed decisions: legacy=%s/%s/%s v2=%s/%s/%s, equal_updated=%v (SQLite timestamp precision)",
		legacy.Status, legacy.DecidedBy, legacy.UpdatedAt.Format(time.RFC3339Nano),
		qualified.Status, qualified.DecidedBy, qualified.UpdatedAt.Format(time.RFC3339Nano), legacy.UpdatedAt.Equal(qualified.UpdatedAt))
	if legacy.Status != "denied" || qualified.Status != "approved" || legacy.DecidedBy != "parent-link" || qualified.DecidedBy != "parent-link" {
		t.Fatalf("unexpected signed results: legacy=%+v v2=%+v", legacy, qualified)
	}
	var qualifiedSequence int64
	if err := db.SQL().QueryRow(`SELECT decision_sequence FROM approvals WHERE id=?`, v2ID).Scan(&qualifiedSequence); err != nil {
		t.Fatal(err)
	}
	other := turn("file_write")
	if !errors.Is(other.ToolCalls[0].Error, agentcore.ErrToolBlocked) || calls != 0 {
		t.Fatalf("legacy must block other implementation: %+v calls=%d", other.ToolCalls, calls)
	}
	approved := turn("builtin__file_write")
	if approved.ToolCalls[0].Error != nil || calls != 1 {
		t.Fatalf("later signed specific approval should execute exactly once, got error=%v disposition=%v calls=%d", approved.ToolCalls[0].Error, approved.ToolCalls[0].Disposition, calls)
	}
	if _, err := db.SQL().Exec(`UPDATE approvals SET expires_at=? WHERE id=?`, time.Now().Add(-time.Hour), v2ID); err != nil {
		t.Fatal(err)
	}
	if result := turn("builtin__file_write"); !errors.Is(result.ToolCalls[0].Error, agentcore.ErrToolBlocked) || calls != 1 {
		t.Fatalf("expired qualified approval must remain blocked: %+v calls=%d", result.ToolCalls, calls)
	}
	if _, err := db.SQL().Exec(`UPDATE approvals SET expires_at=?, decision_sequence=0 WHERE id=?`, time.Now().Add(time.Hour), v2ID); err != nil {
		t.Fatal(err)
	}
	if result := turn("builtin__file_write"); !errors.Is(result.ToolCalls[0].Error, agentcore.ErrToolBlocked) || calls != 1 {
		t.Fatalf("unknown decision order must remain blocked: %+v calls=%d", result.ToolCalls, calls)
	}
	// Recreate the pending legacy state to exercise the opposite signed order:
	// the qualified grant is older than the subsequent legacy denial.
	if _, err := db.SQL().Exec(`UPDATE approvals SET decision_sequence=? WHERE id=?`, qualifiedSequence, v2ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE approvals SET status='pending', decided_by='', decision_sequence=0 WHERE id=?`, legacyID); err != nil {
		t.Fatal(err)
	}
	decide(legacyID, "deny")
	if result := turn("builtin__file_write"); !errors.Is(result.ToolCalls[0].Error, agentcore.ErrToolBlocked) || calls != 1 {
		t.Fatalf("later signed legacy denial must override older qualified approval: %+v calls=%d", result.ToolCalls, calls)
	}
}

func TestSignedQualifiedApprovalAfterLegacyDatabaseMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE approvals (id TEXT PRIMARY KEY, user_name TEXT NOT NULL, user_display TEXT NOT NULL, age_group TEXT NOT NULL, category TEXT NOT NULL, query_text TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME NOT NULL, decided_by TEXT, decision_note TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": "old.sh", "content": "#!/bin/sh\necho hi"}
	oldID := legacyApprovalTestID("kid", "file_write", args)
	newID := "v2-" + legacyApprovalTestID("kid", "builtin__file_write", args)
	for _, row := range []struct{ id, status, actor string }{{oldID, "denied", "parent-link"}, {newID, "pending", ""}} {
		_, err = raw.Exec(`INSERT INTO approvals(id,user_name,user_display,age_group,category,query_text,status,expires_at,decided_by) VALUES(?,?,?,?,?,?,?,?,?)`, row.id, "kid", "Kid", "age_8_12", "tool:file_write", "test", row.status, time.Now().Add(time.Hour), row.actor)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evaluator, err := policy.NewEvaluator("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Secret: "test-only-secret"}, Approval: config.ApprovalConfig{ExpiryHours: 24}}
	server := NewServer(ctx, cfg, "", db, nil, nil, nil, evaluator, nil, nil, nil, nil, nil)
	defer server.bgWG.Wait()
	rec := httptest.NewRecorder()
	token := notify.GenerateToken(newID, "approve", cfg.Server.Secret)
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/decide?token="+url.QueryEscape(token), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("signed approval status=%d", rec.Code)
	}
	server.bgWG.Wait()
	calls := 0
	deps := agentcore.ToolLoopDeps{PolicyEvaluator: evaluator, ApprovalStore: db, MaxIterations: 1, ClientFactory: func(*agentcore.Turn) llm.Chatter { return localReapprovalChat{} }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) { calls++; return "ok", nil }}
	invoke := func(name string) *agentcore.Turn {
		t.Helper()
		turn := &agentcore.Turn{User: &config.UserConfig{Name: "kid", Role: "child", AgeGroup: "age_8_12"}, Tools: []agentcore.Tool{{Name: "builtin__file_write", Source: "builtin"}, {Name: "file_write", Source: "mcp"}}}
		turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "call", Function: llm.ToolCallFunction{Name: name, Arguments: args}}})
		turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
		if err := agentcore.NewStageToolLoop(deps)(ctx, turn); err != nil {
			t.Fatal(err)
		}
		if len(turn.ToolCalls) != 1 {
			t.Fatal("missing tool result")
		}
		return turn
	}
	if turn := invoke("builtin__file_write"); turn.ToolCalls[0].Error != nil || calls != 1 {
		t.Fatalf("new signed grant blocked: %+v calls=%d", turn.ToolCalls, calls)
	}
	if turn := invoke("file_write"); !errors.Is(turn.ToolCalls[0].Error, agentcore.ErrToolBlocked) || calls != 1 {
		t.Fatalf("legacy denial must still block other identity: %+v calls=%d", turn.ToolCalls, calls)
	}
}
