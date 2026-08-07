package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func noScanReg(dir string) *skillbridge.Registry {
	return skillbridge.NewRegistry(dir, nil, skillbridge.InstallConfig{}, nil)
}

func TestSkillInstallAndList(t *testing.T) {
	regDir, srcDir := setupSkillDir(t)
	reg := noScanReg(regDir)

	skill, err := reg.Install(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if skill.Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", skill.Name)
	}

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
	reg := noScanReg(t.TempDir())
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
	reg := noScanReg(regDir)

	reg.Install(context.Background(), srcDir)
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
	reg := noScanReg(regDir)

	reg.Install(context.Background(), srcDir)

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
		return
	}
	if filepath.Base(dir) != "skills" {
		t.Errorf("expected dir ending in 'skills', got %q", dir)
	}
}

// TestSkillInstallRefusesWhenScannerUnavailable verifies that the CLI install
// path (cmd/famclaw/skills.go → skillInstall) refuses when seccheck.enabled is
// true but the honeybadger scanner cannot be made available. This is the
// fail-closed path described in the README: "When the scanner genuinely cannot
// be made available, FamClaw fails closed, never waving a skill through
// unscanned."
//
// We make the scanner unavailable deterministically by stripping the Go
// toolchain from PATH — EnsureScanner checks exec.LookPath("go") first and
// returns an error when it is absent, so no network call is attempted.
func TestSkillInstallRefusesWhenScannerUnavailable(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config with seccheck.enabled: true (no auto_seccheck)
	homeDir := filepath.Join(tmpDir, "home")
	famclawDir := filepath.Join(homeDir, ".famclaw")
	if err := os.MkdirAll(famclawDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(famclawDir, "config.yaml"),
		[]byte("seccheck:\n  enabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a test skill to install
	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(testSkillMD), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(tmpDir, "skills")

	// Point HOME at our temp dir so findConfigFile discovers the config.
	t.Setenv("HOME", homeDir)
	// Strip the Go toolchain (and honeybadger) from PATH so EnsureScanner
	// cannot fetch or build the scanner — it fails immediately at the
	// exec.LookPath("go") check.
	t.Setenv("PATH", "/usr/bin:/bin")

	err := skillInstall(skillsDir, srcDir)
	if err == nil {
		t.Fatal("expected skillInstall to return an error when scanner unavailable, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "scanner") && !strings.Contains(errMsg, "seccheck") {
		t.Errorf("error should mention scanner/seccheck, got: %v", err)
	}

	// Nothing should have been written to the skills dir.
	entries, _ := os.ReadDir(skillsDir)
	if len(entries) != 0 {
		t.Errorf("expected empty skills dir after refused install, got %d entries", len(entries))
	}
}

// TestSkillInstallConfigLoadError verifies that a corrupted config file is
// not silently swallowed — the install fails explicitly rather than falling
// back to seccheck-disabled defaults.
func TestSkillInstallConfigLoadError(t *testing.T) {
	tmpDir := t.TempDir()

	homeDir := filepath.Join(tmpDir, "home")
	famclawDir := filepath.Join(homeDir, ".famclaw")
	if err := os.MkdirAll(famclawDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a deliberately invalid YAML config.
	if err := os.WriteFile(
		filepath.Join(famclawDir, "config.yaml"),
		[]byte("seccheck: { enabled: [unclosed"), 0644); err != nil {
		t.Fatal(err)
	}

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(testSkillMD), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(tmpDir, "skills")

	t.Setenv("HOME", homeDir)
	// PATH doesn't matter for this test — the config parse fails before
	// we ever reach the scanner.

	err := skillInstall(skillsDir, srcDir)
	if err == nil {
		t.Fatal("expected error when config file is corrupted, got nil")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("error should mention config, got: %v", err)
	}
}

// TestSkillInstallNoConfigNoScan verifies that when no config file is found,
// skillInstall proceeds with seccheck disabled (the explicit-opt-in model).
func TestSkillInstallNoConfigNoScan(t *testing.T) {
	tmpDir := t.TempDir()

	homeDir := filepath.Join(tmpDir, "home")
	t.Setenv("HOME", homeDir) // No config file in this HOME

	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "SKILL.md"), []byte(testSkillMD), 0644); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(tmpDir, "skills")

	err := skillInstall(skillsDir, srcDir)
	if err != nil {
		t.Fatalf("expected install to succeed with no config (seccheck disabled), got: %v", err)
	}

	// Verify the skill was installed.
	skills, _ := os.ReadDir(skillsDir)
	if len(skills) != 1 {
		t.Errorf("expected 1 installed skill, got %d", len(skills))
	}
}
