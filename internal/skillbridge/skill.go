// Package skillbridge parses SKILL.md files and manages installed skills.
// Compatible with OpenClaw and PicoClaw SKILL.md format.
package skillbridge

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name        string   `yaml:"name"        json:"name"`
	Description string   `yaml:"description" json:"description"`
	Version     string   `yaml:"version"     json:"version,omitempty"`
	Author      string   `yaml:"author"      json:"author,omitempty"`
	Tags        []string `yaml:"tags"        json:"tags,omitempty"`
	Platforms   []string `yaml:"platforms"   json:"platforms,omitempty"`
	Requires    struct {
		Bins []string `yaml:"bins" json:"bins,omitempty"`
	} `yaml:"requires" json:"requires,omitempty"`
	Body string `yaml:"-" json:"-"`              // raw body after frontmatter
	Path string `yaml:"-" json:"path,omitempty"` // filesystem path of SKILL.md
}

// ParseSKILLMD reads and parses a SKILL.md file at the given path.
func ParseSKILLMD(path string) (*Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md %q: %w", path, err)
	}
	return ParseSKILLMDContent(string(raw), path)
}

// ParseSKILLMDContent parses SKILL.md content from a string.
func ParseSKILLMDContent(content, path string) (*Skill, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("SKILL.md must start with --- frontmatter delimiter")
	}

	// Split frontmatter from body
	rest := content[3:] // skip first ---
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return nil, fmt.Errorf("SKILL.md missing closing --- frontmatter delimiter")
	}

	frontmatter := strings.TrimSpace(rest[:endIdx])
	body := strings.TrimSpace(rest[endIdx+4:]) // skip \n---

	var skill Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &skill); err != nil {
		return nil, fmt.Errorf("parsing SKILL.md frontmatter: %w", err)
	}

	if skill.Name == "" {
		return nil, fmt.Errorf("SKILL.md missing required field: name")
	}

	skill.Body = body
	skill.Path = path
	return &skill, nil
}
