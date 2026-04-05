// Package skilladapt provides multi-format skill adapters for FamClaw.
// Supports skills from FamClaw (SKILL.md), OpenClaw (SOUL.md), and
// Claude Code (agent .md) ecosystems.
package skilladapt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is the internal normalized representation of a skill from any ecosystem.
type Skill struct {
	Name        string
	Description string
	Version     string
	Author      string
	Tags        []string
	Tools       []string     // tools this skill needs (for smart selection)
	Trigger     SkillTrigger // when to inject
	Body        string       // instructions for LLM context
	Format      string       // origin: "famclaw", "openclaw", "claudecode"
	Path        string       // source file path
}

// SkillTrigger determines when a skill is injected into the pipeline.
type SkillTrigger struct {
	Mode     string   // "always" | "keyword" | "classifier" | "manual"
	Keywords []string // for keyword mode
	Category string   // for classifier mode
}

// SkillAdapter can detect and parse a skill format.
type SkillAdapter interface {
	Detect(path string) bool
	Parse(path string) (*Skill, error)
	FormatName() string
}

var adapters = []SkillAdapter{
	&FamClawAdapter{},
	&OpenClawAdapter{},
	&ClaudeCodeAdapter{},
}

// DetectAndParse tries each adapter in order and returns the first match.
func DetectAndParse(path string) (*Skill, error) {
	for _, a := range adapters {
		if a.Detect(path) {
			return a.Parse(path)
		}
	}
	return nil, fmt.Errorf("no adapter recognized %q", path)
}

// DetectFormat returns the format name for a skill path, or empty string if unknown.
func DetectFormat(path string) string {
	for _, a := range adapters {
		if a.Detect(path) {
			return a.FormatName()
		}
	}
	return ""
}

// parseFrontmatter splits a markdown file into frontmatter key-value pairs and body.
func parseFrontmatter(content string) (map[string]string, string) {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil, content
	}

	fm := make(map[string]string)
	bodyStart := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			bodyStart = i + 1
			break
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = strings.Trim(val, `"'`)
			fm[key] = val
		}
	}

	if bodyStart < 0 || bodyStart >= len(lines) {
		return fm, ""
	}
	return fm, strings.Join(lines[bodyStart:], "\n")
}

// parseList parses a YAML-style list value: "[a, b, c]" or "a, b, c"
func parseList(s string) []string {
	s = strings.Trim(s, "[] ")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// fileExists checks if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// findFile looks for a specific filename in a directory.
func findFile(dir, name string) string {
	path := filepath.Join(dir, name)
	if fileExists(path) {
		return path
	}
	return ""
}
