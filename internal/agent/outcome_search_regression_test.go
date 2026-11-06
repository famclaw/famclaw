package agent

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/websearch"
)

func TestOutcomeSearchDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{"failure", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			// "fake-*" fixture: synthetic test marker, not a live secret. The
			// check-private-details guard (CREDENTIAL shape) only exempts the
			// documented placeholder conventions (fake-*/test-*/changeme/...),
			// so keep this value inside that allowlist.
			const secret = "fake-search-secret"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			chatter := &staleSearchChatter{script: []*llm.Message{{Role: "assistant", Content: "done"}}}
			turn := &agentcore.Turn{User: &config.UserConfig{Name: "tester", Role: "child"}, Tools: []agentcore.Tool{{Name: "builtin__web_search"}}}
			turn.SetMeta("pending_tool_calls", []llm.ToolCall{{ID: "search-id", Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "sample"}}}})
			turn.SetMeta("llm_messages", []llm.Message{{Role: "user", Content: "search"}})
			stage := agentcore.NewStageToolLoop(agentcore.ToolLoopDeps{MaxIterations: 1, ClientFactory: func(*agentcore.Turn) llm.Chatter { return chatter }, BuiltinHandler: func(context.Context, string, map[string]any) (string, error) {
				if !tc.failed {
					return "sample result", nil
				}
				return webSearchError(fmt.Errorf("%s: %w", secret, websearch.ErrProvider5xx))
			}})
			if err := stage(context.Background(), turn); err != nil {
				t.Fatal(err)
			}
			if len(turn.ToolCalls) != 1 {
				t.Fatal("missing tool result")
			}
			if tc.failed {
				if turn.ToolCalls[0].Error == nil {
					t.Error("classified search failure recorded as success")
				}
				if strings.Contains(logs.String(), secret) {
					t.Error("private search cause leaked to logs")
				}
				seenMarker := false
				for _, batch := range chatter.recorded {
					for _, m := range batch {
						if m.Role == "tool" && strings.Contains(m.Content, "web_search: search backend error") {
							seenMarker = true
						}
						if strings.Contains(m.Content, secret) {
							t.Error("private search cause leaked to model")
						}
					}
				}
				if !seenMarker {
					t.Error("classified marker missing from model transcript")
				}
			} else if turn.ToolCalls[0].Error != nil || turn.ToolCalls[0].Output != "sample result" {
				t.Error("successful search result not preserved")
			}
		})
	}
}
