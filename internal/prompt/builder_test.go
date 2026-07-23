package prompt

import (
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestBuild_Smoke(t *testing.T) {
	ctx := BuildContext{
		Cfg:  &config.Config{},
		User: &config.UserConfig{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
	}
	out := Build(ctx)
	if out == "" {
		t.Fatal("Build returned empty string for non-empty context")
	}
	if !strings.Contains(out, "FamClaw") {
		t.Errorf("expected output to contain 'FamClaw', got: %q", out)
	}
}

func TestUserComponent(t *testing.T) {
	cases := []struct {
		name     string
		user     *config.UserConfig
		wantSubs []string
	}{
		{
			name:     "child with age group",
			user:     &config.UserConfig{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
			wantSubs: []string{"Julia", "child"},
		},
		{
			name:     "parent",
			user:     &config.UserConfig{Name: "dep", DisplayName: "Dep", Role: "parent"},
			wantSubs: []string{"Dep", "parent"},
		},
		{
			name:     "child no age group",
			user:     &config.UserConfig{Name: "kid", DisplayName: "Kid", Role: "child"},
			wantSubs: []string{"Kid", "child"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, ok := userComponent(BuildContext{User: tc.user})
			if !ok || text == "" {
				t.Fatalf("expected included, got ok=%v text=%q", ok, text)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(text, sub) {
					t.Errorf("expected %q in: %q", sub, text)
				}
			}
		})
	}
}

func TestUserComponent_NilUser(t *testing.T) {
	text, ok := userComponent(BuildContext{User: nil})
	if ok || text != "" {
		t.Errorf("expected excluded for nil user, got ok=%v text=%q", ok, text)
	}
}

func TestFamilyComponent(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "dep", DisplayName: "Dep", Role: "parent"},
			{Name: "julia", DisplayName: "Julia", Role: "child"},
			{Name: "teo", DisplayName: "Teo", Role: "child"},
		},
	}
	text, ok := familyComponent(BuildContext{Cfg: cfg, User: &cfg.Users[1]})
	if !ok {
		t.Fatal("expected included")
	}
	for _, sub := range []string{"Dep (parent)", "Teo (child)"} {
		if !strings.Contains(text, sub) {
			t.Errorf("missing %q in %q", sub, text)
		}
	}
	if strings.Contains(text, "Julia") {
		t.Errorf("self should not appear in family list: %q", text)
	}

	// Single user — excluded
	cfg2 := &config.Config{Users: []config.UserConfig{cfg.Users[0]}}
	if _, ok := familyComponent(BuildContext{Cfg: cfg2, User: &cfg2.Users[0]}); ok {
		t.Error("expected excluded for single-user family")
	}
}

func TestAgeComponent(t *testing.T) {
	cases := []struct {
		ageGroup string
		included bool
		sub      string
	}{
		{"under_8", true, "simple words"},
		{"age_8_12", true, "Be friendly"},
		{"age_13_17", true, "teenager"},
		{"", false, ""},
		{"unknown_bucket", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.ageGroup, func(t *testing.T) {
			user := &config.UserConfig{DisplayName: "X", AgeGroup: tc.ageGroup}
			text, ok := ageComponent(BuildContext{User: user})
			if ok != tc.included {
				t.Fatalf("included=%v want %v (text=%q)", ok, tc.included, text)
			}
			if tc.included && !strings.Contains(text, tc.sub) {
				t.Errorf("expected %q in %q", tc.sub, text)
			}
		})
	}
}

func TestPolicyComponent(t *testing.T) {
	if _, ok := policyComponent(BuildContext{HardBlocked: nil}); ok {
		t.Error("expected excluded with no hard-blocked categories")
	}
	text, ok := policyComponent(BuildContext{HardBlocked: []string{"weapons", "self_harm"}})
	if !ok {
		t.Fatal("expected included")
	}
	for _, sub := range []string{"weapons", "self_harm", "cannot be discussed"} {
		if !strings.Contains(text, sub) {
			t.Errorf("missing %q in %q", sub, text)
		}
	}
}

func TestApprovalsComponent_ParentVsChild(t *testing.T) {
	parent := &config.UserConfig{Role: "parent"}
	if _, ok := approvalsComponent(BuildContext{User: parent}); ok {
		t.Error("approvals component should be excluded for parents")
	}
	child := &config.UserConfig{Role: "child"}
	text, ok := approvalsComponent(BuildContext{User: child})
	if !ok {
		t.Fatal("expected included for child")
	}
	if !strings.Contains(text, "approval") {
		t.Errorf("missing approval mention: %q", text)
	}
}

func TestCapabilitiesComponent_AlwaysOnBaseline(t *testing.T) {
	text, ok := capabilitiesComponent(BuildContext{})
	if !ok || text == "" {
		t.Fatal("capabilities should always include baseline")
	}
	if !strings.Contains(text, "yes") && !strings.Contains(text, "can") {
		t.Errorf("expected positive capability statement, got: %q", text)
	}
}

func TestSkillsComponent(t *testing.T) {
	if _, ok := skillsComponent(BuildContext{Skills: nil}); ok {
		t.Error("expected excluded with no skills")
	}
	text, ok := skillsComponent(BuildContext{Skills: []string{"seccheck", "honeybadger"}})
	if !ok {
		t.Fatal("expected included")
	}
	for _, sub := range []string{"seccheck", "honeybadger"} {
		if !strings.Contains(text, sub) {
			t.Errorf("missing %q", sub)
		}
	}
}

func TestGatewayComponent(t *testing.T) {
	cases := map[string]bool{
		"telegram": true, "discord": true, "web": true, "": false, "irc": false,
	}
	for gw, want := range cases {
		t.Run(gw, func(t *testing.T) {
			_, ok := gatewayComponent(BuildContext{Gateway: gw})
			if ok != want {
				t.Errorf("gateway %q: ok=%v want %v", gw, ok, want)
			}
		})
	}
}

func TestOutputComponent_AlwaysOn(t *testing.T) {
	text, ok := outputComponent(BuildContext{})
	if !ok {
		t.Fatal("expected always included")
	}
	if !strings.Contains(text, "concise") && !strings.Contains(text, "short") {
		t.Errorf("expected length guidance: %q", text)
	}
}

func TestMemoryComponent_PlaceholderExcluded(t *testing.T) {
	// Memory is not yet implemented — component should be excluded.
	if _, ok := memoryComponent(BuildContext{}); ok {
		t.Error("memory component should be excluded until memory feature lands")
	}
}

// approxTokens estimates token count from char count. Rough heuristic:
// English text averages ~4 chars/token in Anthropic/OpenAI BPE tokenizers.
// Off by 10-15% but stable enough for a regression budget.
func approxTokens(s string) int {
	return len(s) / 4
}

func TestBuild_ParentTokenBudget(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "dep", DisplayName: "Dep", Role: "parent"},
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
			{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "under_8"},
		},
	}
	out := Build(BuildContext{
		Cfg:         cfg,
		User:        &cfg.Users[0],
		Gateway:     "telegram",
		Skills:      []string{"seccheck", "honeybadger"},
		HardBlocked: []string{"weapons", "self_harm", "drugs"},
	})
	tokens := approxTokens(out)
	if tokens > 900 {
		t.Fatalf("parent prompt over budget: %d tokens (limit 900)\n---\n%s", tokens, out)
	}
	t.Logf("parent prompt: %d tokens, %d chars", tokens, len(out))
}

func TestBuild_ChildTokenBudget(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "dep", DisplayName: "Dep", Role: "parent"},
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
		},
	}
	out := Build(BuildContext{
		Cfg:         cfg,
		User:        &cfg.Users[1],
		Gateway:     "telegram",
		HardBlocked: []string{"weapons"},
	})
	tokens := approxTokens(out)
	if tokens > 650 {
		t.Fatalf("child prompt over budget: %d tokens (limit 650)\n---\n%s", tokens, out)
	}
	t.Logf("child prompt: %d tokens, %d chars", tokens, len(out))
}

func TestCapabilitiesComponent_BuiltinTools(t *testing.T) {
	t.Run("web_fetch only", func(t *testing.T) {
		text, ok := capabilitiesComponent(BuildContext{BuiltinTools: []string{"web_fetch"}})
		if !ok || text == "" {
			t.Fatalf("expected included, got ok=%v text=%q", ok, text)
		}
		for _, want := range []string{"web_fetch", "current events"} {
			if !strings.Contains(text, want) {
				t.Errorf("expected %q in %q", want, text)
			}
		}
	})
	t.Run("spawn_agent only", func(t *testing.T) {
		text, ok := capabilitiesComponent(BuildContext{BuiltinTools: []string{"spawn_agent"}})
		if !ok || text == "" {
			t.Fatalf("expected included, got ok=%v text=%q", ok, text)
		}
		for _, want := range []string{"spawn_agent", "Delegate"} {
			if !strings.Contains(text, want) {
				t.Errorf("expected %q in %q", want, text)
			}
		}
	})
	t.Run("both", func(t *testing.T) {
		text, _ := capabilitiesComponent(BuildContext{BuiltinTools: []string{"web_fetch", "spawn_agent"}})
		if !strings.Contains(text, "web_fetch") {
			t.Errorf("missing web_fetch in: %q", text)
		}
		if !strings.Contains(text, "spawn_agent") {
			t.Errorf("missing spawn_agent in: %q", text)
		}
	})
	t.Run("empty", func(t *testing.T) {
		text, _ := capabilitiesComponent(BuildContext{})
		if strings.Contains(text, "web_fetch") || strings.Contains(text, "spawn_agent") {
			t.Errorf("baseline should not mention specific tools, got: %q", text)
		}
	})
	t.Run("behavioral rules included with tools", func(t *testing.T) {
		text, _ := capabilitiesComponent(BuildContext{BuiltinTools: []string{"web_fetch"}})
		// Tool-call format guardrail — prevents <tool_call> XML leak
		// to user-visible reply (Nemotron-style trailing block).
		for _, want := range []string{"tool call FIRST", "do NOT append another tool_call", "Never write the literal text"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing tool-format rule %q in capabilities text", want)
			}
		}
		// Anti-hallucination guardrails.
		for _, want := range []string{"ONLY summarize what the tool actually returned", "I couldn't find that information", "don't have current data"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing grounding rule %q in capabilities text", want)
			}
		}
	})
	t.Run("behavioral rules omitted when no tools", func(t *testing.T) {
		text, _ := capabilitiesComponent(BuildContext{})
		// No tools = no tool-call guardrails (they wouldn't apply).
		if strings.Contains(text, "<tool_call>") || strings.Contains(text, "ONLY summarize what the tool") {
			t.Errorf("toolless capabilities should not include tool-call rules, got: %q", text)
		}
	})
	t.Run("web_search only", func(t *testing.T) {
		text, ok := capabilitiesComponent(BuildContext{BuiltinTools: []string{"web_search"}})
		if !ok || text == "" {
			t.Fatalf("expected included, got ok=%v text=%q", ok, text)
		}
		// Check for the specific hint
		if !strings.Contains(text, "Search the web via web_search") {
			t.Errorf("expected web_search hint in: %q", text)
		}
		// Ensure it does not contain the generic fallback
		if strings.Contains(text, "Use the web_search tool when relevant") {
			t.Errorf("should not contain generic web_search hint: %q", text)
		}
	})
}

func TestBehavioralRules_Exported(t *testing.T) {
	rules := BehavioralRules()
	if rules == "" {
		t.Fatal("BehavioralRules() must not return empty")
	}
	for _, want := range []string{"tool call FIRST", "ONLY summarize"} {
		if !strings.Contains(rules, want) {
			t.Errorf("exported BehavioralRules() missing %q", want)
		}
	}
}

func TestBuild_TokenBudget_WithBuiltinTools(t *testing.T) {
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "dep", DisplayName: "Dep", Role: "parent"},
			{Name: "julia", DisplayName: "Julia", Role: "child", AgeGroup: "age_8_12"},
			{Name: "teo", DisplayName: "Teo", Role: "child", AgeGroup: "under_8"},
		},
	}
	out := Build(BuildContext{
		Cfg:          cfg,
		User:         &cfg.Users[0],
		Gateway:      "telegram",
		HardBlocked:  []string{"weapons"},
		BuiltinTools: []string{"spawn_agent", "web_fetch"},
	})
	tokens := approxTokens(out)
	if tokens > 900 {
		t.Fatalf("parent prompt with builtin tools over budget: %d tokens (limit 900)\n---\n%s", tokens, out)
	}
	t.Logf("parent prompt with builtin tools: %d tokens, %d chars", tokens, len(out))
}
