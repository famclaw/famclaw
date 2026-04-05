package skilladapt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeCodeAdapter parses Claude Code agent markdown files.
// Detected by: .md file with "description:" in frontmatter but no "name:" or "soul:".
type ClaudeCodeAdapter struct{}

func (a *ClaudeCodeAdapter) FormatName() string { return "claudecode" }

func (a *ClaudeCodeAdapter) Detect(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		// Look for any .md file with description frontmatter
		entries, err := os.ReadDir(path)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
				data, err := os.ReadFile(filepath.Join(path, e.Name()))
				if err != nil {
					continue
				}
				fm, _ := parseFrontmatter(string(data))
				if fm != nil && fm["description"] != "" && fm["name"] == "" && fm["soul"] == "" {
					return true
				}
			}
		}
		return false
	}

	// Single file
	if !strings.HasSuffix(path, ".md") {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, _ := parseFrontmatter(string(data))
	return fm != nil && fm["description"] != "" && fm["name"] == "" && fm["soul"] == ""
}

func (a *ClaudeCodeAdapter) Parse(path string) (*Skill, error) {
	info, _ := os.Stat(path)
	if info != nil && info.IsDir() {
		// Find the first matching .md file
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
				candidate := filepath.Join(path, e.Name())
				data, err := os.ReadFile(candidate)
				if err != nil {
					continue
				}
				fm, _ := parseFrontmatter(string(data))
				if fm != nil && fm["description"] != "" && fm["name"] == "" && fm["soul"] == "" {
					path = candidate
					break
				}
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading Claude Code skill: %w", err)
	}

	fm, body := parseFrontmatter(string(data))
	if fm == nil {
		return nil, fmt.Errorf("no frontmatter in %s", path)
	}

	// Derive name from filename
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ToLower(name)

	skill := &Skill{
		Name:        name,
		Description: fm["description"],
		Tags:        parseList(fm["tags"]),
		Tools:       parseList(fm["tools"]),
		Body:        body,
		Format:      "claudecode",
		Path:        path,
		Trigger:     SkillTrigger{Mode: "manual"},
	}

	return skill, nil
}
