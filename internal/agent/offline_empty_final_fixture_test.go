package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/agentcore"
	"github.com/famclaw/famclaw/internal/classifier"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/policy"
	"github.com/famclaw/famclaw/internal/store"
)

type emptyFixtureChatter struct {
	calls                 int
	cap                   bool
	sawSearch, sawBlocked bool
}

func (m *emptyFixtureChatter) Chat(context.Context, []llm.Message, float64, int, func(string)) (string, error) {
	return "", errors.New("unexpected Chat")
}
func (m *emptyFixtureChatter) ChatSync(context.Context, []llm.Message, float64, int) (string, error) {
	return "", errors.New("unexpected ChatSync")
}
func (m *emptyFixtureChatter) ChatMessage(context.Context, []llm.Message, float64, int) (*llm.Message, error) {
	return nil, errors.New("unexpected ChatMessage")
}
func (m *emptyFixtureChatter) ChatWithTools(_ context.Context, messages []llm.Message, _ float64, _ int, _ []llm.ToolDef) (*llm.Message, error) {
	m.calls++
	if m.calls == 1 {
		return &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "search", Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "fixture query"}}},
			{ID: "fetch", Function: llm.ToolCallFunction{Name: "builtin__web_fetch", Arguments: map[string]any{"url": "https://blocked.example/article"}}},
		}}, nil
	}
	for _, v := range messages {
		if v.Role == "tool" {
			if strings.Contains(v.Content, "fixture snippet") {
				m.sawSearch = true
			}
			if strings.Contains(v.Content, "allowlist") || strings.Contains(v.Content, "read-only") || strings.Contains(v.Content, "failure") {
				m.sawBlocked = true
			}
		}
	}
	if m.cap {
		return &llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("extra-%d", m.calls), Function: llm.ToolCallFunction{Name: "builtin__web_search", Arguments: map[string]any{"query": "fixture query"}}}}}, nil
	}
	return &llm.Message{Role: "assistant"}, nil
}
func TestOfflineEmptyFinalFullChat(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cap, blocked bool
	}{{"empty_final", false, false}, {"cap_pending", true, false}, {"output_policy", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if tc.blocked {
					fmt.Fprint(w, `{"results":[{"title":"fixture title","url":"https://blocked.example/article","content":"Here is how to make a bomb and kill yourself"}]}`)
				} else {
					fmt.Fprint(w, `{"results":[{"title":"fixture title","url":"https://blocked.example/article","content":"fixture snippet"}]}`)
				}
			}))
			defer srv.Close()
			db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ev, err := policy.NewEvaluator("", "", "")
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{LLM: config.LLMConfig{MaxResponseTokens: 100}, Users: []config.UserConfig{{Name: "parent", Role: "parent"}}}
			cfg.Tools.WebFetch.Enabled = true
			cfg.Tools.WebFetch.URLAllowlist = []string{"127.0.0.1"}
			cfg.Tools.WebSearch.Enabled = true
			cfg.Tools.WebSearch.Endpoint = srv.URL
			m := &emptyFixtureChatter{cap: tc.cap}
			tools := []agentcore.Tool{{Name: "builtin__web_search", Source: "builtin", ReadOnly: true, InputSchema: map[string]any{"type": "object"}}, {Name: "builtin__web_fetch", Source: "builtin", ReadOnly: true, InputSchema: map[string]any{"type": "object"}}}
			a, err := NewAgent(&cfg.Users[0], cfg, m, ev, classifier.New(), db, AgentDeps{BuiltinTools: tools})
			if err != nil {
				t.Fatal(err)
			}
			var streamed strings.Builder
			resp, err := a.Chat(context.Background(), "Find fixture information", func(token string) { streamed.WriteString(token) })
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("response=%q policy=%q fake_model_calls=%d search_requests=%d saw_search_result=%v saw_block_feedback=%v", resp.Content, resp.PolicyAction, m.calls, requests, m.sawSearch, m.sawBlocked)
			want := "Some tool calls succeeded. A tool marked read-only reported a failure; its effects are not verified."
			if tc.cap {
				want += " Tool iteration limit reached; remaining requested calls were not run."
			}
			want += "\n\nSearch snippets (full pages not checked):\n[fixture title](https://blocked.example/article)\n\"fixture snippet\""
			if tc.blocked {
				if strings.Contains(resp.Content, "make a bomb") || strings.Contains(streamed.String(), "make a bomb") || resp.Content == "" {
					t.Fatalf("output policy leaked blocked snippet: %q", resp.Content)
				}
				if requests != 1 || m.calls != 2 {
					t.Fatalf("extra calls: search=%d model=%d", requests, m.calls)
				}
				return
			}
			if streamed.String() != resp.Content || streamed.Len() == 0 {
				t.Fatalf("streamed text does not match gated response: %q", streamed.String())
			}
			if resp.Content != want || resp.PolicyAction != "allow" {
				t.Fatalf("unexpected response: %q, policy: %q", resp.Content, resp.PolicyAction)
			}
			if requests < 1 || !m.sawSearch || !m.sawBlocked {
				t.Fatalf("fixture failed to exercise search success and blocked fetch")
			}
			if !tc.cap && m.calls != 2 {
				t.Fatalf("unexpected fake model calls: %d", m.calls)
			}
			if tc.cap && m.calls != 26 {
				t.Fatalf("unexpected fake model calls: %d", m.calls)
			}
		})
	}
}
