package agentcore

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
)

func TestToolCallLogAttributionWithoutArguments(t *testing.T) {
	for _, tc := range []struct {
		name        string
		user        string
		denied      bool
		disposition ToolDisposition
	}{
		{name: "failed call", user: "alice", disposition: DispositionAttempted},
		{name: "denied call", user: "bob", denied: true, disposition: DispositionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "fake-private-marker"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })
			tools := []Tool{{Name: "builtin__operation"}}
			if tc.denied {
				tools = []Tool{{Name: "builtin__other"}}
			}
			turn := &Turn{User: &config.UserConfig{Name: tc.user, Role: "child"}, Tools: tools}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "test-id", Function: llm.ToolCallFunction{Name: "builtin__operation", Arguments: map[string]any{"token": secret}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "go"}})
			handlerCalls := 0
			deps := ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*Turn) llm.Chatter { return &leakChatter{} }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				handlerCalls++
				return "", errors.New(secret)
			}}
			if err := NewStageToolLoop(deps)(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Disposition != tc.disposition {
				t.Fatalf("unexpected tool outcome: %+v", turn.ToolCalls)
			}
			if tc.denied && handlerCalls != 0 || !tc.denied && handlerCalls != 1 {
				t.Fatalf("handler calls = %d", handlerCalls)
			}
			want := "[agentcore][" + tc.user + "] tool_call: builtin__operation"
			if !strings.Contains(logs.String(), want) {
				t.Errorf("missing attributed tool-call log for %s", tc.user)
			}
			if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "args=") {
				t.Error("tool arguments or private error leaked to logs")
			}
		})
	}
}
