package skilladapt

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenClawAdapter parses SOUL.md files (OpenClaw/NemoClaw format).
type OpenClawAdapter struct{}

func (a *OpenClawAdapter) FormatName() string { return "openclaw" }

func (a *OpenClawAdapter) Detect(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return findFile(path, "SOUL.md") != ""
	}
	return filepath.Base(path) == "SOUL.md"
}

func (a *OpenClawAdapter) Parse(path string) (*Skill, error) {
	info, _ := os.Stat(path)
	if info != nil && info.IsDir() {
		path = filepath.Join(path, "SOUL.md")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SOUL.md: %w", err)
	}

	fm, body := parseFrontmatter(string(data))
	if fm == nil {
		return nil, fmt.Errorf("no frontmatter in %s", path)
	}

	// Map OpenClaw fields to FamClaw skill
	skill := &Skill{
		Name:        fm["soul"],
		Description: fm["description"],
		Version:     fm["version"],
		Author:      fm["author"],
		Tags:        parseList(fm["tags"]),
		Tools:       parseList(fm["tools"]),
		Body:        body,
		Format:      "openclaw",
		Path:        path,
	}

	// OpenClaw triggers
	if triggers := fm["triggers"]; triggers != "" {
		skill.Trigger.Keywords = parseList(triggers)
		skill.Trigger.Mode = "keyword"
	} else {
		skill.Trigger.Mode = "manual"
	}

	if skill.Name == "" {
		return nil, fmt.Errorf("SOUL.md at %s missing 'soul' in frontmatter", path)
	}

	return skill, nil
}
