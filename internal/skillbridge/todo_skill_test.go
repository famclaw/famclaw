package skillbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTodoSKILLMD(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")

	// Use double-quoted strings for \n escape sequences
	content := "---\n" +
		"name: todo\n" +
		"description: Manage personal and family todo lists via chat\n" +
		"version: \"0.1\"\n" +
		"author: famclaw\n" +
		"tags: [productivity, family, todo]\n" +
		"platforms: [linux, darwin]\n" +
		"requires:\n" +
		"  bins: []\n" +
		"trigger:\n" +
		"  mode: \"keyword\"\n" +
		"  keywords: [\"todo\", \"task\", \"list\", \"add\", \"complete\", \"done\", \"remove\", \"delete\"]\n" +
		"\n" +
		"---\n" +
		"# Todo Skill\n\n" +
		"Manage personal and family todo lists via chat. Todos are scoped per user -- each family member has their own list.\n\n" +
		"## When to use\n\n" +
		"Use this skill when the user wants to:\n" +
		"- Add items to their todo list (\"add milk to my list\", \"remember to buy eggs\")\n" +
		"- List their todo items (\"what's on my todo list\", \"show my tasks\")\n" +
		"- Mark items as complete (\"mark milk done\", \"complete buy eggs\")\n" +
		"- Remove items (\"remove milk from my list\", \"delete buy eggs\")\n\n" +
		"## How to invoke\n\n" +
		"Use the \"builtin__todo\" tool with the appropriate action:\n"

	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	skill, err := ParseSKILLMD(skillPath)
	if err != nil {
		t.Fatalf("ParseSKILLMD: %v", err)
	}

	if skill.Name != "todo" {
		t.Errorf("Name = %q, want todo", skill.Name)
	}
	if skill.Description != "Manage personal and family todo lists via chat" {
		t.Errorf("Description = %q", skill.Description)
	}
	if skill.Version != "0.1" {
		t.Errorf("Version = %q, want 0.1", skill.Version)
	}
	if skill.Author != "famclaw" {
		t.Errorf("Author = %q, want famclaw", skill.Author)
	}
	if len(skill.Tags) != 3 {
		t.Errorf("Tags = %v, want 3 tags", skill.Tags)
	}
	if len(skill.Platforms) != 2 {
		t.Errorf("Platforms = %v, want 2 platforms", skill.Platforms)
	}
	if len(skill.Requires.Bins) != 0 {
		t.Errorf("Requires.Bins = %v, want empty", skill.Requires.Bins)
	}
	if skill.Path != skillPath {
		t.Errorf("Path = %q, want %q", skill.Path, skillPath)
	}
	// Check body contains key sections
	if !containsAll(skill.Body, []string{"When to use", "How to invoke", "builtin__todo"}) {
		t.Errorf("Body missing expected sections: %s", skill.Body)
	}
}

func containsAll(s string, substrings []string) bool {
	for _, sub := range substrings {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr)))
}