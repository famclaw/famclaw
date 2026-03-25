package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/famclaw/famclaw/internal/skillbridge"
)

const testSkillMD = `---
name: test-skill
description: A test skill
version: "0.1"
tags: [test]
---
Test skill body.
`

func setupSkillDir(t *testing.T) (registryDir string, srcDir string) {
	t.Helper()
	registryDir = t.TempDir()
	srcDir = t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(testSkillMD), 0644)
	return
}

func TestSkillInstallAndList(t *testing.T) {
	regDir, srcDir := setupSkillDir(t)
	reg := skillbridge.NewRegistry(regDir)

	// Install
	skill, err := reg.Install(srcDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if skill.Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", skill.Name)
	}

	// List
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "test-skill" {
		t.Errorf("listed name = %q", skills[0].Name)
	}
}

func TestSkillListEmpty(t *testing.T) {
	reg := skillbridge.NewRegistry(t.TempDir())
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0, got %d", len(skills))
	}
}

func TestSkillInstallAndRemove(t *testing.T) {
	regDir, srcDir := setupSkillDir(t)
	reg := skillbridge.NewRegistry(regDir)

	reg.Install(srcDir)
	if err := reg.Remove("test-skill"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	skills, _ := reg.List()
	if len(skills) != 0 {
		t.Errorf("expected 0 after remove, got %d", len(skills))
	}
}

func TestSkillEnableDisable(t *testing.T) {
	regDir, srcDir := setupSkillDir(t)
	reg := skillbridge.NewRegistry(regDir)

	reg.Install(srcDir)

	if !reg.IsEnabled("test-skill") {
		t.Error("should be enabled by default")
	}

	reg.Disable("test-skill")
	if reg.IsEnabled("test-skill") {
		t.Error("should be disabled")
	}

	reg.Enable("test-skill")
	if !reg.IsEnabled("test-skill") {
		t.Error("should be re-enabled")
	}
}

func TestDefaultSkillsDir(t *testing.T) {
	dir := defaultSkillsDir()
	if dir == "" {
		t.Error("defaultSkillsDir should not be empty")
	}
	if !filepath.IsAbs(dir) {
		// May be relative if UserHomeDir fails
		return
	}
	if filepath.Base(dir) != "skills" {
		t.Errorf("expected dir ending in 'skills', got %q", dir)
	}
}
