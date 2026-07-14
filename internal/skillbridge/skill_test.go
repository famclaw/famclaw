package skillbridge

import (
	"os"
	"testing"

	"github.com/famclaw/famclaw/internal/skilladapt"
)

func TestParseUserMemorySKILLMD(t *testing.T) {
	skillDir := "../../skills-repo/user-memory"
	skillMDPath := skillDir + "/SKILL.md"

	// Verify file exists
	if _, err := os.Stat(skillMDPath); os.IsNotExist(err) {
		t.Fatalf("SKILL.md not found at %s", skillMDPath)
	}

	// Parse using the skilladapt package (which handles both formats)
	adaptSkill, err := skilladapt.DetectAndParse(skillDir)
	if err != nil {
		t.Fatalf("DetectAndParse failed: %v", err)
	}

	// Verify required fields
	if adaptSkill.Name != "user-memory" {
		t.Errorf("expected name 'user-memory', got %q", adaptSkill.Name)
	}
	if adaptSkill.Description == "" {
		t.Error("expected non-empty description")
	}
	if adaptSkill.Version == "" {
		t.Error("expected non-empty version")
	}
	if adaptSkill.Body == "" {
		t.Error("expected non-empty body")
	}

	// Verify trigger mode is "always"
	if adaptSkill.Trigger.Mode != "always" {
		t.Errorf("expected trigger mode 'always', got %q", adaptSkill.Trigger.Mode)
	}

	// Verify body contains key sections
	expectedSections := []string{
		"When to Use User Memory",
		"REMEMBER",
		"RECALL",
		"FORGET",
		"remember_user_memory",
		"recall_user_memory",
		"forget_user_memory",
	}
	for _, section := range expectedSections {
		if !contains(adaptSkill.Body, section) {
			t.Errorf("skill body missing expected section: %q", section)
		}
	}

	// Test ParseSKILLMDContent directly as well
	raw, err := os.ReadFile(skillMDPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	skill, err := ParseSKILLMDContent(string(raw), skillMDPath)
	if err != nil {
		t.Fatalf("ParseSKILLMDContent failed: %v", err)
	}
	if skill.Name != "user-memory" {
		t.Errorf("expected name 'user-memory', got %q", skill.Name)
	}
	if skill.Description != adaptSkill.Description {
		t.Errorf("description mismatch: %q vs %q", skill.Description, adaptSkill.Description)
	}
	if skill.Body != adaptSkill.Body {
		t.Error("body mismatch between ParseSKILLMDContent and DetectAndParse")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}