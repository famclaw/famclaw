package skilladapt

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFamClawAdapter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "SKILL.md", `---
name: seccheck
description: Security scanner for skills
version: "1.0"
author: famclaw
tags: [security, skills]
tools: [web_search, file_read]
trigger: keyword
keywords: [scan, security, check]
---
# SecCheck

Run security checks on skills before installing.
`)

	adapter := &FamClawAdapter{}
	if !adapter.Detect(dir) {
		t.Fatal("should detect SKILL.md in directory")
	}

	skill, err := adapter.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "seccheck" {
		t.Errorf("name = %q, want 'seccheck'", skill.Name)
	}
	if skill.Format != "famclaw" {
		t.Errorf("format = %q, want 'famclaw'", skill.Format)
	}
	if len(skill.Tags) != 2 {
		t.Errorf("tags = %v, want 2", skill.Tags)
	}
	if len(skill.Tools) != 2 {
		t.Errorf("tools = %v, want 2", skill.Tools)
	}
	if skill.Trigger.Mode != "keyword" {
		t.Errorf("trigger.mode = %q, want 'keyword'", skill.Trigger.Mode)
	}
	if len(skill.Trigger.Keywords) != 3 {
		t.Errorf("trigger.keywords = %v, want 3", skill.Trigger.Keywords)
	}
	if skill.Body == "" {
		t.Error("body should not be empty")
	}
}

func TestOpenClawAdapter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "SOUL.md", `---
soul: math-tutor
description: Helps with math homework
version: "2.1"
author: openclaw
tags: [education, math]
tools: [calculator]
triggers: [math, homework, equation]
---
# Math Tutor

I help students solve math problems step by step.
`)

	adapter := &OpenClawAdapter{}
	if !adapter.Detect(dir) {
		t.Fatal("should detect SOUL.md in directory")
	}

	skill, err := adapter.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "math-tutor" {
		t.Errorf("name = %q, want 'math-tutor'", skill.Name)
	}
	if skill.Format != "openclaw" {
		t.Errorf("format = %q, want 'openclaw'", skill.Format)
	}
	if skill.Trigger.Mode != "keyword" {
		t.Errorf("trigger.mode = %q, want 'keyword'", skill.Trigger.Mode)
	}
	if len(skill.Trigger.Keywords) != 3 {
		t.Errorf("keywords = %v, want 3", skill.Trigger.Keywords)
	}
}

func TestClaudeCodeAdapter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "code-review.md", `---
description: Reviews code for bugs and security issues
tags: [code, review]
---
# Code Review Agent

Analyze code changes and provide detailed review feedback.
`)

	adapter := &ClaudeCodeAdapter{}
	if !adapter.Detect(dir) {
		t.Fatal("should detect Claude Code agent .md in directory")
	}

	skill, err := adapter.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Name != "code-review" {
		t.Errorf("name = %q, want 'code-review'", skill.Name)
	}
	if skill.Format != "claudecode" {
		t.Errorf("format = %q, want 'claudecode'", skill.Format)
	}
	if skill.Description != "Reviews code for bugs and security issues" {
		t.Errorf("description = %q", skill.Description)
	}
	if skill.Trigger.Mode != "manual" {
		t.Errorf("trigger.mode = %q, want 'manual'", skill.Trigger.Mode)
	}
}

func TestDetectAndParse(t *testing.T) {
	// FamClaw format
	dir := t.TempDir()
	writeFile(t, dir, "SKILL.md", `---
name: test-skill
description: A test skill
---
Body content.
`)

	skill, err := DetectAndParse(dir)
	if err != nil {
		t.Fatalf("DetectAndParse: %v", err)
	}
	if skill.Name != "test-skill" {
		t.Errorf("name = %q, want 'test-skill'", skill.Name)
	}
	if skill.Format != "famclaw" {
		t.Errorf("format = %q, want 'famclaw'", skill.Format)
	}
}

func TestDetectAndParseUnknown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Just a readme")

	_, err := DetectAndParse(dir)
	if err == nil {
		t.Error("expected error for unrecognized format")
	}
}

func TestDetectFormat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "SKILL.md", "---\nname: test\n---\n")

	format := DetectFormat(dir)
	if format != "famclaw" {
		t.Errorf("DetectFormat = %q, want 'famclaw'", format)
	}

	format = DetectFormat(filepath.Join(dir, "nonexistent"))
	if format != "" {
		t.Errorf("DetectFormat(nonexistent) = %q, want ''", format)
	}
}

func TestParseFrontmatter(t *testing.T) {
	content := "---\nname: test\ndescription: A test\ntags: [a, b]\n---\nBody here."
	fm, body := parseFrontmatter(content)
	if fm["name"] != "test" {
		t.Errorf("name = %q", fm["name"])
	}
	if fm["description"] != "A test" {
		t.Errorf("description = %q", fm["description"])
	}
	if body != "Body here." {
		t.Errorf("body = %q", body)
	}
}

func TestParseList(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"[a, b, c]", 3},
		{"a, b", 2},
		{"", 0},
		{"[single]", 1},
	}
	for _, tt := range tests {
		got := parseList(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseList(%q) = %v (len %d), want len %d", tt.input, got, len(got), tt.want)
		}
	}
}
