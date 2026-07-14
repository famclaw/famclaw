package skillbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSKILLMD = `---
name: seccheck
description: Scan a skill or MCP git repository for security issues
version: "1.0"
author: famclaw
tags: [security, skills, mcp]
platforms: [linux, darwin]
requires:
  bins: [seccheck]
---
# SecCheck Skill

When the user asks to check a skill or scan a repo, run the seccheck binary.

## Usage
` + "```" + `
seccheck <repo-url>
` + "```" + `

Report the findings back to the user.
`

const minimalSKILLMD = `---
name: minimal
description: A minimal skill
version: "0.1"
tags: []
---
Do something simple.
`

func TestParseSKILLMDContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
		check   func(t *testing.T, s *Skill)
	}{
		{
			name:    "full skill",
			content: testSKILLMD,
			check: func(t *testing.T, s *Skill) {
				if s.Name != "seccheck" {
					t.Errorf("Name = %q, want seccheck", s.Name)
				}
				if s.Version != "1.0" {
					t.Errorf("Version = %q, want 1.0", s.Version)
				}
				if s.Author != "famclaw" {
					t.Errorf("Author = %q, want famclaw", s.Author)
				}
				if len(s.Tags) != 3 {
					t.Errorf("Tags = %v, want 3 tags", s.Tags)
				}
				if len(s.Platforms) != 2 {
					t.Errorf("Platforms = %v, want 2", s.Platforms)
				}
				if len(s.Requires.Bins) != 1 || s.Requires.Bins[0] != "seccheck" {
					t.Errorf("Requires.Bins = %v, want [seccheck]", s.Requires.Bins)
				}
				if !strings.Contains(s.Body, "SecCheck Skill") {
					t.Error("Body should contain skill instructions")
				}
			},
		},
		{
			name:    "minimal skill",
			content: minimalSKILLMD,
			check: func(t *testing.T, s *Skill) {
				if s.Name != "minimal" {
					t.Errorf("Name = %q, want minimal", s.Name)
				}
				if s.Body != "Do something simple." {
					t.Errorf("Body = %q", s.Body)
				}
			},
		},
		{
			name:    "missing frontmatter start",
			content: "no frontmatter here",
			wantErr: true,
		},
		{
			name:    "missing frontmatter end",
			content: "---\nname: test\n",
			wantErr: true,
		},
		{
			name:    "missing name field",
			content: "---\ndescription: no name\n---\nbody",
			wantErr: true,
		},
		{
			name:    "empty content",
			content: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skill, err := ParseSKILLMDContent(tt.content, "test.md")
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, skill)
			}
		})
	}
}

func TestParseSKILLMDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	os.WriteFile(path, []byte(testSKILLMD), 0644)

	skill, err := ParseSKILLMD(path)
	if err != nil {
		t.Fatalf("ParseSKILLMD: %v", err)
	}
	if skill.Name != "seccheck" {
		t.Errorf("Name = %q, want seccheck", skill.Name)
	}
	if skill.Path != path {
		t.Errorf("Path = %q, want %q", skill.Path, path)
	}
}

func TestParseSKILLMDFileNotFound(t *testing.T) {
	_, err := ParseSKILLMD("/nonexistent/SKILL.md")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadForPromptEmpty(t *testing.T) {
	result := LoadForPrompt(nil)
	if result != "" {
		t.Errorf("empty skills should return empty string, got %q", result)
	}
}

func TestLoadForPromptSingle(t *testing.T) {
	skill := &Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0",
		Body:        "Do the thing.\nWith multiple lines.",
	}

	result := LoadForPrompt([]*Skill{skill})

	if !strings.HasPrefix(result, "<AgentSkills>") {
		t.Error("should start with <AgentSkills>")
	}
	if !strings.HasSuffix(result, "</AgentSkills>") {
		t.Error("should end with </AgentSkills>")
	}
	if !strings.Contains(result, `name="test-skill"`) {
		t.Error("should contain skill name attribute")
	}
	if !strings.Contains(result, `description="A test skill"`) {
		t.Error("should contain description attribute")
	}
	if !strings.Contains(result, `version="1.0"`) {
		t.Error("should contain version attribute")
	}
	if !strings.Contains(result, "Do the thing.") {
		t.Error("body should be injected verbatim")
	}
	if !strings.Contains(result, "With multiple lines.") {
		t.Error("multi-line body should be preserved")
	}
}

func TestLoadForPromptMultiple(t *testing.T) {
	skills := []*Skill{
		{Name: "skill-a", Body: "A instructions"},
		{Name: "skill-b", Body: "B instructions"},
	}

	result := LoadForPrompt(skills)

	if strings.Count(result, "<Skill ") != 2 {
		t.Errorf("expected 2 Skill elements, got %d", strings.Count(result, "<Skill "))
	}
	if !strings.Contains(result, `name="skill-a"`) {
		t.Error("missing skill-a")
	}
	if !strings.Contains(result, `name="skill-b"`) {
		t.Error("missing skill-b")
	}
}

func TestRegistryInstallListRemove(t *testing.T) {
	registryDir := t.TempDir()
	reg := NewRegistry(registryDir, nil, InstallConfig{}, nil)

	// Create a skill source
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(testSKILLMD), 0644)

	// Install
	skill, err := reg.Install(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if skill.Name != "seccheck" {
		t.Errorf("installed skill name = %q", skill.Name)
	}

	// List
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "seccheck" {
		t.Errorf("listed skill = %q", skills[0].Name)
	}

	// Remove
	if err := reg.Remove("seccheck"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	skills, _ = reg.List()
	if len(skills) != 0 {
		t.Errorf("after remove, expected 0 skills, got %d", len(skills))
	}
}

func TestRegistryRemoveNonexistent(t *testing.T) {
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{}, nil)
	err := reg.Remove("nonexistent")
	if err == nil {
		t.Error("expected error removing nonexistent skill")
	}
}

func TestRegistryEnableDisable(t *testing.T) {
	registryDir := t.TempDir()
	reg := NewRegistry(registryDir, nil, InstallConfig{}, nil)

	// Create a skill source and install
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(minimalSKILLMD), 0644)
	reg.Install(context.Background(), srcDir)

	// Initially enabled
	if !reg.IsEnabled("minimal") {
		t.Error("skill should be enabled by default")
	}

	// Disable
	if err := reg.Disable("minimal"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if reg.IsEnabled("minimal") {
		t.Error("skill should be disabled")
	}

	// Re-enable
	if err := reg.Enable("minimal"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !reg.IsEnabled("minimal") {
		t.Error("skill should be re-enabled")
	}
}

func TestRegistryDisableNonexistent(t *testing.T) {
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{}, nil)
	err := reg.Disable("ghost")
	if err == nil {
		t.Error("expected error disabling nonexistent skill")
	}
}

func TestRegistryListEmptyDir(t *testing.T) {
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{}, nil)
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List on empty dir: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestRegistryListNonexistentDir(t *testing.T) {
	reg := NewRegistry("/nonexistent/path", nil, InstallConfig{}, nil)
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List on nonexistent dir should not error: %v", err)
	}
	if skills != nil {
		t.Errorf("expected nil, got %v", skills)
	}
}

func TestBodyInjectedVerbatim(t *testing.T) {
	body := "Line 1\n  indented\n\ttabbed\n<special> & \"chars\""
	skill := &Skill{Name: "verbatim", Body: body}
	result := LoadForPrompt([]*Skill{skill})

	// Every line of body should appear in output
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(result, line) {
			t.Errorf("body line %q not found verbatim in output", line)
		}
	}
}

func TestValidateNameEmpty(t *testing.T) {
	err := ValidateName("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidateNamePathTraversal(t *testing.T) {
	tests := []string{
		"../../.ssh",
		"../../../etc/cron.d/x",
		"../secret",
		"./evil",
		"foo/..",
		"foo\\..",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err == nil {
				t.Fatalf("expected error for path traversal name %q", name)
			}
		})
	}
}

func TestValidateNameControlChars(t *testing.T) {
	tests := []string{
		"foo\x00bar",
		"foo\x01bar",
		"foo\nbar",
		"foo\x7fbar",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err == nil {
				t.Fatalf("expected error for name with control char %q", name)
			}
		})
	}
}

func TestValidateNameSpecialChars(t *testing.T) {
	tests := []string{
		"foo/bar",
		"foo\\bar",
		"foo..bar",
		"foo bar",
		"foo@bar",
		"foo+bar",
		"foo#bar",
		"foo bar.md",
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err == nil {
				t.Fatalf("expected error for name %q", name)
			}
		})
	}
}

func TestValidateNameValid(t *testing.T) {
	valid := []string{
		"seccheck",
		"my-skill",
		"my_skill",
		"MySkill",
		"MYSKILL",
		"skill123",
		"a",
		"A",
		"abc-def_123",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			err := ValidateName(name)
			if err != nil {
				t.Fatalf("unexpected error for valid name %q: %v", name, err)
			}
		})
	}
}

func TestValidateNameTooLong(t *testing.T) {
	longName := strings.Repeat("a", 65)
	err := ValidateName(longName)
	if err == nil {
		t.Fatal("expected error for 65-char name")
	}
}

func TestValidateInstalledDirEscape(t *testing.T) {
	base := "/skills"
	candidate := "/skills/../../.ssh"
	err := ValidateInstalledDir(base, candidate)
	if err == nil {
		t.Fatal("expected error for directory escaping base")
	}
}

func TestValidateInstalledDirSafe(t *testing.T) {
	base := "/skills"
	candidate := "/skills/my-skill"
	err := ValidateInstalledDir(base, candidate)
	if err != nil {
		t.Fatalf("unexpected error for safe directory: %v", err)
	}
}

func TestInstallPathTraversal(t *testing.T) {
	registryDir := t.TempDir()
	reg := NewRegistry(registryDir, nil, InstallConfig{}, nil)

	// SKILL.md with path traversal in name
	traversalSKILLMD := `---
name: ../../.ssh
description: Evil skill
---
Evil content.
`
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(traversalSKILLMD), 0644)

	_, err := reg.Install(context.Background(), srcDir)
	if err == nil {
		t.Fatal("expected error installing skill with traversal in name")
	}
	if !strings.Contains(err.Error(), "invalid skill name") {
		t.Errorf("expected 'invalid skill name' in error, got: %v", err)
	}

	// Verify nothing was written outside the skills dir
	entries, _ := os.ReadDir(registryDir)
	if len(entries) != 0 {
		t.Errorf("expected no files in skills dir, found: %v", entries)
	}
}

func TestInstallValidName(t *testing.T) {
	registryDir := t.TempDir()
	reg := NewRegistry(registryDir, nil, InstallConfig{}, nil)

	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(minimalSKILLMD), 0644)

	skill, err := reg.Install(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill.Name != "minimal" {
		t.Errorf("name = %q, want minimal", skill.Name)
	}

	// Verify the skill was installed into its own directory
	_, err = os.Stat(filepath.Join(registryDir, "minimal", "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md not found at expected path: %v", err)
	}
}

// TestRegistryRoleEnablement tests the role-based enablement functionality.
func TestRegistryRoleEnablement(t *testing.T) {
	// Create a temporary directory for the registry.
	registryDir := t.TempDir()

	// Create a role enablement map: parent gets skill1 and skill2, child gets only skill1.
	roleEnablement := map[string][]string{
		"parent": {"skill1", "skill2"},
		"child":  {"skill1"},
	}

	// Create a registry with the role enablement.
	reg := NewRegistry(registryDir, nil, InstallConfig{}, roleEnablement)

	// Install three skills: skill1, skill2, skill3.
	// Skill1: no .disabled -> should be enabled for parent and child (because in parent's list and child's list).
	// Skill2: no .disabled -> should be enabled for parent only.
	// Skill3: with .disabled -> should be disabled for all roles.
	for _, name := range []string{"skill1", "skill2", "skill3"} {
		skillDir := filepath.Join(registryDir, name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("failed to create skill dir: %v", err)
		}
		// Write a minimal SKILL.md.
		skillMD := filepath.Join(skillDir, "SKILL.md")
		if err := os.WriteFile(skillMD, []byte(fmt.Sprintf(`---
name: %s
description: A test skill
version: "0.1"
---
Test skill body.`, name)), 0644); err != nil {
			t.Fatalf("failed to write SKILL.md: %v", err)
		}
		// For skill3, also create a .disabled file to make it globally disabled.
		if name == "skill3" {
			disabledFile := filepath.Join(skillDir, ".disabled")
			if err := os.WriteFile(disabledFile, []byte("disabled"), 0644); err != nil {
				t.Fatalf("failed to write .disabled file: %v", err)
			}
		}
	}

	// Test ListForRole for parent.
	parentSkills, err := reg.ListForRole("parent")
	if err != nil {
		t.Fatalf("ListForRole(parent) failed: %v", err)
	}
	if len(parentSkills) != 2 {
		t.Errorf("expected 2 skills for parent, got %d", len(parentSkills))
	}
	// Check that skill1 and skill2 are present.
	foundSkill1 := false
	foundSkill2 := false
	for _, s := range parentSkills {
		switch s.Name {
		case "skill1":
			foundSkill1 = true
		case "skill2":
			foundSkill2 = true
		case "skill3":
			t.Errorf("unexpectedly found skill3 in parent skills")
		}
	}
	if !foundSkill1 {
		t.Error("skill1 not found in parent skills")
	}
	if !foundSkill2 {
		t.Error("skill2 not found in parent skills")
	}

	// Test ListForRole for child.
	childSkills, err := reg.ListForRole("child")
	if err != nil {
		t.Fatalf("ListForRole(child) failed: %v", err)
	}
	if len(childSkills) != 1 {
		t.Errorf("expected 1 skill for child, got %d", len(childSkills))
	}
	// Check that skill1 is present and skill2 is not.
	foundSkill1InChild := false
	foundSkill2InChild := false
	for _, s := range childSkills {
		switch s.Name {
		case "skill1":
			foundSkill1InChild = true
		case "skill2":
			foundSkill2InChild = true
		case "skill3":
			t.Errorf("unexpectedly found skill3 in child skills")
		}
	}
	if !foundSkill1InChild {
		t.Error("skill1 not found in child skills")
	}
	if foundSkill2InChild {
		t.Error("skill2 unexpectedly found in child skills")
	}

	// Test IsEnabledFor for each skill and role.
	tests := []struct {
		name  string
		role  string
		want1 bool // skill1
		want2 bool // skill2
		want3 bool // skill3
	}{
		{"parent", "parent", true, true, false},
		{"child", "child", true, false, false},
		// Also test a role with no config: should fall back to global.
		{"guest", "guest", true, true, false}, // skill1 and skill2 are globally enabled, skill3 is disabled.
	}
	for _, tt := range tests {
		if got := reg.IsEnabledFor("skill1", tt.role); got != tt.want1 {
			t.Errorf("IsEnabledFor(skill1, %s) = %v, want %v", tt.role, got, tt.want1)
		}
		if got := reg.IsEnabledFor("skill2", tt.role); got != tt.want2 {
			t.Errorf("IsEnabledFor(skill2, %s) = %v, want %v", tt.role, got, tt.want2)
		}
		if got := reg.IsEnabledFor("skill3", tt.role); got != tt.want3 {
			t.Errorf("IsEnabledFor(skill3, %s) = %v, want %v", tt.role, got, tt.want3)
		}
	}

	// Test that global disable overrides role enablement.
	// Even though skill3 is not in any role enablement list, it should be disabled because of .disabled.
	// We already tested that in the IsEnabledFor above (want3 is false for all roles).
	// Also test that if we remove the .disabled file, then skill3 should be enabled for roles that have it in their enablement list.
	// But note: we cannot easily remove the .disabled file without changing the filesystem.
	// Instead, we'll test that if a skill is not globally disabled and not in the role enablement list for a role with config, then it is disabled for that role.
	// For role "parent", skill3 is not in the enablement list -> should be disabled (and we already want false).
	// For role "child", skill3 is not in the enablement list -> should be disabled (we want false).
	// For role "guest", there is no enablement config -> should fall back to global -> disabled because of .disabled -> we want false (already in the test).
}
