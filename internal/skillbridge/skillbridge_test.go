package skillbridge

import (
	"context"
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
	reg := NewRegistry(registryDir, nil, InstallConfig{})

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
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{})
	err := reg.Remove("nonexistent")
	if err == nil {
		t.Error("expected error removing nonexistent skill")
	}
}

func TestRegistryEnableDisable(t *testing.T) {
	registryDir := t.TempDir()
	reg := NewRegistry(registryDir, nil, InstallConfig{})

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
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{})
	err := reg.Disable("ghost")
	if err == nil {
		t.Error("expected error disabling nonexistent skill")
	}
}

func TestRegistryListEmptyDir(t *testing.T) {
	reg := NewRegistry(t.TempDir(), nil, InstallConfig{})
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List on empty dir: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestRegistryListNonexistentDir(t *testing.T) {
	reg := NewRegistry("/nonexistent/path", nil, InstallConfig{})
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
