package skilladapt

import (
	"fmt"
	"os"
	"path/filepath"
)

// FamClawAdapter parses SKILL.md files (FamClaw native format).
type FamClawAdapter struct{}

func (a *FamClawAdapter) FormatName() string { return "famclaw" }

func (a *FamClawAdapter) Detect(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return findFile(path, "SKILL.md") != ""
	}
	return filepath.Base(path) == "SKILL.md"
}

func (a *FamClawAdapter) Parse(path string) (*Skill, error) {
	info, _ := os.Stat(path)
	if info != nil && info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	fm, body := parseFrontmatter(string(data))
	if fm == nil {
		return nil, fmt.Errorf("no frontmatter in %s", path)
	}

	skill := &Skill{
		Name:        fm["name"],
		Description: fm["description"],
		Version:     fm["version"],
		Author:      fm["author"],
		Tags:        parseList(fm["tags"]),
		Tools:       parseList(fm["tools"]),
		Body:        body,
		Format:      "famclaw",
		Path:        path,
	}

	// Parse trigger
	if mode := fm["trigger"]; mode != "" {
		skill.Trigger.Mode = mode
	} else {
		skill.Trigger.Mode = "manual"
	}
	if kw := fm["keywords"]; kw != "" {
		skill.Trigger.Keywords = parseList(kw)
		if skill.Trigger.Mode == "manual" {
			skill.Trigger.Mode = "keyword"
		}
	}

	if skill.Name == "" {
		return nil, fmt.Errorf("SKILL.md at %s missing 'name' in frontmatter", path)
	}

	return skill, nil
}
