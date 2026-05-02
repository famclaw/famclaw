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

func TestOAuthPrefixComponent(t *testing.T) {
	if _, ok := oauthPrefixComponent(BuildContext{OAuth: false}); ok {
		t.Error("oauth prefix should be excluded when OAuth=false")
	}
	text, ok := oauthPrefixComponent(BuildContext{OAuth: true})
	if !ok || text == "" {
		t.Fatal("expected included for OAuth=true")
	}
}
