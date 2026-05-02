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
