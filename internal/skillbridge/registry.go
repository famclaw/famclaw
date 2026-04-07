package skillbridge

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/skilladapt"
)

// Scanner is the minimal interface Registry needs from HoneyBadger.
type Scanner interface {
	Available() bool
	Scan(ctx context.Context, target string, opts honeybadger.ScanOptions) (*honeybadger.ScanResult, error)
}

// InstallConfig controls security scanning during skill installation.
type InstallConfig struct {
	Enabled      bool   // seccheck.enabled master switch
	AutoSecCheck bool   // seccheck.auto_seccheck
	BlockOnFail  bool   // seccheck.block_on_fail
	Paranoia     string // seccheck.paranoia
}

// Registry manages installed skills on disk.
type Registry struct {
	dir     string
	scanner Scanner       // may be nil if scanning is disabled
	cfg     InstallConfig
}

// NewRegistry creates a Registry rooted at the given directory.
// scanner may be nil if seccheck is disabled.
func NewRegistry(dir string, scanner Scanner, cfg InstallConfig) *Registry {
	return &Registry{dir: dir, scanner: scanner, cfg: cfg}
}

// Install parses, scans (if configured), and installs a skill.
func (r *Registry) Install(ctx context.Context, nameOrPath string) (*Skill, error) {
	// Ensure skills dir exists
	if err := os.MkdirAll(r.dir, 0755); err != nil {
		return nil, fmt.Errorf("creating skills dir: %w", err)
	}

	// Parse the skill first to get name/metadata
	var skill *Skill
	adaptSkill, adaptErr := skilladapt.DetectAndParse(nameOrPath)
	if adaptErr == nil {
		skill = adaptSkillToSkill(adaptSkill)
	} else {
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

	// Security scan before writing anything to disk
	if r.cfg.Enabled && r.cfg.AutoSecCheck {
		if r.scanner == nil || !r.scanner.Available() {
			return nil, fmt.Errorf(
				"honeybadger is required for skill installation but is not available\n" +
					"install: go install github.com/famclaw/honeybadger/cmd/honeybadger@latest\n" +
					"or disable in config.yaml: seccheck.auto_seccheck: false (not recommended)")
		}

		result, err := r.scanner.Scan(ctx, nameOrPath, honeybadger.ScanOptions{
			Paranoia: r.cfg.Paranoia,
		})
		if err != nil {
			return nil, fmt.Errorf("security scan failed: %w", err)
		}

		switch result.Verdict {
		case "FAIL":
			if r.cfg.BlockOnFail {
				return nil, fmt.Errorf(
					"skill rejected by security scan: %s\nreasoning: %s\nkey finding: %s",
					result.Verdict, result.Reasoning, result.KeyFinding)
			}
			log.Printf("[skillbridge] WARNING: %s FAILED scan but block_on_fail=false, installing anyway", nameOrPath)
		case "WARN":
			log.Printf("[skillbridge] WARNING: %s has security warnings: %s", nameOrPath, result.Reasoning)
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
		adaptSkill, err := skilladapt.DetectAndParse(dir)
		if err == nil {
			skills = append(skills, adaptSkillToSkill(adaptSkill))
			continue
		}
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
