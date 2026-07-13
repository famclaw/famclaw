// Package skillbridge parses SKILL.md files and manages installed skills.
// Compatible with OpenClaw and PicoClaw SKILL.md format.
package skillbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Skill represents a parsed SKILL.md file.
type Skill struct {
	Name        string   `yaml:"name"           json:"name"`
	Description string   `yaml:"description"    json:"description"`
	Version     string   `yaml:"version"        json:"version,omitempty"`
	Author      string   `yaml:"author"         json:"author,omitempty"`
	Tags        []string `yaml:"tags"           json:"tags,omitempty"`
	Platforms   []string `yaml:"platforms"      json:"platforms,omitempty"`
	Requires    struct {
		Bins []string `yaml:"bins" json:"bins,omitempty"`
	} `yaml:"requires" json:"requires,omitempty"`
	EnvAllowlist []string `yaml:"env_allowlist"  json:"env_allowlist,omitempty"`
	Body         string   `yaml:"-"              json:"-"` // raw body after frontmatter
	Path         string   `yaml:"-"              json:"-"` // filesystem path of SKILL.md
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

// nameRegex is the allowlist for valid skill names: alphanumerics (any case),
// hyphens and underscores, 1–64 characters. No path separators, no "..",
// no control characters.
var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ValidateName checks that a skill name is safe to use as a filesystem path
// component. It rejects path separators, "..", control characters, and any
// value that does not match the POSIX-filename allowlist.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name must not be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("skill name must not contain path separators: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("skill name must not contain '..': %q", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("skill name must not contain control characters: %q", name)
		}
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf(
			"skill name must match ^[a-zA-Z0-9_-]{1,64}$ (got %q)", name)
	}
	return nil
}

// ValidateInstalledDir ensures that a directory path derived from a skill name
// is still inside the expected base directory. This is defense-in-depth: it
// catches any edge case the name allowlist misses (e.g. unexpected symlinks).
func ValidateInstalledDir(baseDir, candidateDir string) error {
	cleaned := filepath.Clean(candidateDir)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolving base dir: %w", err)
	}
	absCandidate, err := filepath.Abs(cleaned)
	if err != nil {
		return fmt.Errorf("resolving candidate dir: %w", err)
	}
	rel, err := filepath.Rel(absBase, absCandidate)
	if err != nil {
		return fmt.Errorf("computing relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("skill directory escapes base directory: %q -> %q", candidateDir, rel)
	}
	return nil
}
