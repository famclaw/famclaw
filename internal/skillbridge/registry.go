package skillbridge

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/famclaw/famclaw/internal/skilladapt"
)

// Registry manages installed skills on disk.
type Registry struct {
	dir string // skills directory (e.g. ~/.famclaw/skills/)
}

// NewRegistry creates a Registry rooted at the given directory.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir}
}

// Install downloads and installs a skill from a repo URL.
// If autoSecCheck is true, seccheck must pass before installation.
// For now, Install works with local paths for testing.
func (r *Registry) Install(nameOrPath string) (*Skill, error) {
	// Ensure skills dir exists
	if err := os.MkdirAll(r.dir, 0755); err != nil {
		return nil, fmt.Errorf("creating skills dir: %w", err)
	}

	// Try multi-format detection first (FamClaw, OpenClaw, Claude Code)
	var skill *Skill
	adaptSkill, adaptErr := skilladapt.DetectAndParse(nameOrPath)
	if adaptErr == nil {
		skill = adaptSkillToSkill(adaptSkill)
	} else {
		// Fallback to direct SKILL.md parsing
		skillMDPath := nameOrPath
		if !strings.HasSuffix(nameOrPath, "SKILL.md") {
			skillMDPath = filepath.Join(nameOrPath, "SKILL.md")
		}
		var parseErr error
		skill, parseErr = ParseSKILLMD(skillMDPath)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing skill: %w (multi-format: %w)", parseErr, adaptErr)
		}
	}

	// Copy skill file to registry dir
	destDir := filepath.Join(r.dir, skill.Name)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating skill dir: %w", err)
	}

	srcPath := skill.Path
	if srcPath == "" {
		srcPath = filepath.Join(nameOrPath, "SKILL.md")
	}
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("reading skill: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(srcPath))
	if err := os.WriteFile(destPath, raw, 0644); err != nil {
		return nil, fmt.Errorf("writing skill: %w", err)
	}

	skill.Path = destPath
	return skill, nil
}

// List returns all installed skills.
func (r *Registry) List() ([]*Skill, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills dir: %w", err)
	}

	var skills []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(r.dir, e.Name())
		// Try multi-format detection first
		adaptSkill, err := skilladapt.DetectAndParse(dir)
		if err == nil {
			skills = append(skills, adaptSkillToSkill(adaptSkill))
			continue
		}
		// Fallback to SKILL.md
		skillMD := filepath.Join(dir, "SKILL.md")
		skill, err := ParseSKILLMD(skillMD)
		if err != nil {
			log.Printf("[skillbridge] skip %s: %v", e.Name(), err)
			continue
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

// Remove deletes an installed skill by name.
func (r *Registry) Remove(name string) error {
	dir := filepath.Join(r.dir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed", name)
	}
	return os.RemoveAll(dir)
}

// Enable creates an "enabled" marker for a skill (default state).
func (r *Registry) Enable(name string) error {
	disabledFile := filepath.Join(r.dir, name, ".disabled")
	if err := os.Remove(disabledFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("enabling skill: %w", err)
	}
	return nil
}

// Disable creates a ".disabled" marker file for a skill.
func (r *Registry) Disable(name string) error {
	dir := filepath.Join(r.dir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed", name)
	}
	disabledFile := filepath.Join(dir, ".disabled")
	return os.WriteFile(disabledFile, []byte("disabled"), 0644)
}

// adaptSkillToSkill converts a skilladapt.Skill to a skillbridge.Skill.
func adaptSkillToSkill(s *skilladapt.Skill) *Skill {
	return &Skill{
		Name:        s.Name,
		Description: s.Description,
		Version:     s.Version,
		Author:      s.Author,
		Tags:        s.Tags,
		Body:        s.Body,
		Path:        s.Path,
	}
}

// IsEnabled returns true if the skill is not disabled.
func (r *Registry) IsEnabled(name string) bool {
	disabledFile := filepath.Join(r.dir, name, ".disabled")
	_, err := os.Stat(disabledFile)
	return os.IsNotExist(err)
}
