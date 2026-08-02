package trello

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillMDReferencesAllTools ensures the SKILL.md body advertises every
// tool name this package registers with the MCP server. If the tool names in
// tools.go drift from the documentation, famclaw's prompt injection and the
// MCP pool disagree — so gate that drift here.
func TestSkillMDReferencesAllTools(t *testing.T) {
	body := readSkillMD(t)

	for _, name := range []string{ToolAddCard, ToolListCards, ToolCompleteCard} {
		if !strings.Contains(body, name) {
			t.Errorf("SKILL.md body does not reference tool %q", name)
		}
	}
}

// TestSkillMDFrontmatter verifies the SKILL.md has a valid, famclaw-loadable
// frontmatter: starts with ---, ends the block with ---, and carries the
// required name + description fields used by skillbridge.ParseSKILLMD.
func TestSkillMDFrontmatter(t *testing.T) {
	raw := readSkillMD(t)
	if !strings.HasPrefix(raw, "---") {
		t.Fatal("SKILL.md must start with --- frontmatter delimiter")
	}
	rest := raw[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		t.Fatal("SKILL.md missing closing --- frontmatter delimiter")
	}
	front := rest[:end]

	required := []string{"name:", "description:", "version:", "author:", "tags:", "platforms:", "requires:", "trigger:"}
	for _, key := range required {
		if !strings.Contains(front, key) {
			t.Errorf("frontmatter missing %q", key)
		}
	}
	if !strings.Contains(front, "name: trello") {
		t.Errorf("frontmatter name is not 'trello': %q", front)
	}
}

func readSkillMD(t *testing.T) string {
	t.Helper()
	return string(readSkillMDRaw(t))
}

func readSkillMDRaw(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}
