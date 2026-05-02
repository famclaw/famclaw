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
