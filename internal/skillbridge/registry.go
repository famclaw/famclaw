package skillbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/skilladapt"
)

// ErrScannerUnavailable is returned when a security scan is required but the
// honeybadger binary is not installed and could not be fetched. Callers MUST
// treat this as a hard refusal (fail-closed) — never as silent success.
var ErrScannerUnavailable = errors.New("honeybadger scanner is not available")

// Scanner is the minimal interface Registry needs from HoneyBadger.
type Scanner interface {
	Available() bool
	Scan(ctx context.Context, target string, opts honeybadger.ScanOptions) (*honeybadger.ScanResult, error)
}

// SecCheckReporter persists security scan results. It is implemented by
// internal/store.DB. nil is allowed (scanning runs but reports are not
// persisted — used by the CLI and tests).
type SecCheckReporter interface {
	SaveSecCheckReport(skillID, repoURL, commitSHA string, score int, verdict, summary, reportJSON string) error
	HasFreshSecCheckReport(repoURL string, stale time.Duration) (bool, error)
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
	dir            string
	scanner        Scanner // may be nil if scanning is disabled
	cfg            InstallConfig
	reporter       SecCheckReporter           // may be nil (e.g. CLI)
	roleEnablement map[string]map[string]bool // role -> skillName -> enabled
}

// NewRegistry creates a Registry rooted at the given directory.
// scanner may be nil if seccheck is disabled.
// roleEnablement is a map of role to slice of skill names that are enabled for that role.
// It may be nil or empty.
func NewRegistry(dir string, scanner Scanner, cfg InstallConfig, roleEnablement map[string][]string) *Registry {
	// Convert the roleEnablement map[string][]string to map[string]map[string]bool for fast lookup.
	re := make(map[string]map[string]bool)
	if roleEnablement != nil {
		for role, skills := range roleEnablement {
			skillSet := make(map[string]bool)
			for _, skill := range skills {
				skillSet[skill] = true
			}
			re[role] = skillSet
		}
	}
	return &Registry{dir: dir, scanner: scanner, cfg: cfg, roleEnablement: re}
}

// WithReporter attaches a SecCheckReporter so that scans persist a row to the
// seccheck_reports table. Safe to omit (the CLI and tests pass nil); in that
// case scanning still runs but no report row is written.
func (r *Registry) WithReporter(reporter SecCheckReporter) *Registry {
	r.reporter = reporter
	return r
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

	// Security scan before writing anything to disk. Scanning happens against
	// the ORIGINAL source (nameOrPath), not the installed copy, so a malicious
	// skill is caught before it ever lands on disk. A missing or unavailable
	// scanner is a hard refusal — never an install of an unscanned skill.
	if r.cfg.Enabled && r.cfg.AutoSecCheck {
		scanTarget, online := resolveScanTarget(nameOrPath)
		result, err := r.scan(ctx, skill, scanTarget, online)
		if err != nil {
			if errors.Is(err, ErrScannerUnavailable) {
				return nil, fmt.Errorf("skill install refused: %w\n"+
					"  install manually: go install github.com/famclaw/honeybadger/cmd/honeybadger@latest\n"+
					"  or set seccheck.enabled: false in config.yaml to disable the security gate", err)
			}
			return nil, fmt.Errorf("security scan failed: %w", err)
		}
		r.persistReport(ctx, skill.Name, scanTarget, result)

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

	// Validate skill name is safe before using it as a path component.
	if err := ValidateName(skill.Name); err != nil {
		return nil, fmt.Errorf("invalid skill name: %w", err)
	}

	// Copy skill file to registry dir
	destDir := filepath.Join(r.dir, skill.Name)

	// Defense-in-depth: verify the resolved directory is still inside the
	// skills root, catching symlinks or unexpected filesystem layout.
	if err := ValidateInstalledDir(r.dir, destDir); err != nil {
		return nil, fmt.Errorf("skill directory path invalid: %w", err)
	}

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
	if err := ValidateName(name); err != nil {
		return fmt.Errorf("invalid skill name: %w", err)
	}
	dir := filepath.Join(r.dir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed", name)
	}
	return os.RemoveAll(dir)
}

// Enable marks a skill as enabled. Returns an error if the skill is not installed.
func (r *Registry) Enable(name string) error {
	if err := ValidateName(name); err != nil {
		return fmt.Errorf("invalid skill name: %w", err)
	}
	dir := filepath.Join(r.dir, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed", name)
	}
	disabledFile := filepath.Join(r.dir, name, ".disabled")
	if err := os.Remove(disabledFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("enabling skill: %w", err)
	}
	return nil
}

// Disable creates a ".disabled" marker file for a skill.
func (r *Registry) Disable(name string) error {
	if err := ValidateName(name); err != nil {
		return fmt.Errorf("invalid skill name: %w", err)
	}
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

// IsEnabledFor returns true if the skill is enabled for the given role.
func (r *Registry) IsEnabledFor(name, role string) bool {
	// If globally disabled, not enabled for any role.
	if !r.IsEnabled(name) {
		return false
	}
	// If there is no role-specific enablement, fall back to global (which is enabled, since we passed the global check).
	if r.roleEnablement == nil {
		return true
	}
	// Check if there is an enablement set for this role.
	if skills, ok := r.roleEnablement[role]; ok {
		return skills[name]
	}
	// No enablement config for this role -> fall back to global (enabled).
	return true
}

// ListForRole returns the list of skills enabled for the given role.
func (r *Registry) ListForRole(role string) ([]*Skill, error) {
	skills, err := r.List()
	if err != nil {
		return nil, err
	}
	var enabled []*Skill
	for _, s := range skills {
		if r.IsEnabledFor(s.Name, role) {
			enabled = append(enabled, s)
		}
	}
	return enabled, nil
}

// scan runs the honeybadger scanner against a skill source. It returns
// ErrScannerUnavailable when the scanner is nil or not in PATH — the caller
// MUST treat that as a refusal, not as a pass.
func (r *Registry) scan(ctx context.Context, skill *Skill, target string, online bool) (*honeybadger.ScanResult, error) {
	if r.scanner == nil {
		return nil, ErrScannerUnavailable
	}
	if !r.scanner.Available() {
		return nil, ErrScannerUnavailable
	}
	opts := honeybadger.ScanOptions{
		Paranoia: r.cfg.Paranoia,
		Offline:  !online,
	}
	return r.scanner.Scan(ctx, target, opts)
}

// ScanSkill scans an already-loaded skill and persists the report (when a
// reporter is configured). It is the single entry point used by the boot-load
// path; Install routes through scan()+persistReport directly so it can refuse
// before writing to disk. On a missing/unavailable scanner it returns
// ErrScannerUnavailable — never a fabricated PASS.
func (r *Registry) ScanSkill(ctx context.Context, skill *Skill, target string) (*honeybadger.ScanResult, error) {
	_, online := resolveScanTarget(target)
	result, err := r.scan(ctx, skill, target, online)
	if err != nil {
		return nil, err
	}
	r.persistReport(ctx, skill.Name, target, result)
	return result, nil
}

// ScanAll scans every loaded skill, skipping ones with a fresh report when a
// reporter is configured. A missing/unavailable scanner is logged loudly per
// skill and aggregated into the returned error — it never silently succeeds.
// Used by main.go after List() so pre-installed skills (e.g. family-knowledge)
// are actually scanned on first boot.
func (r *Registry) ScanAll(ctx context.Context, stale time.Duration) error {
	skills, err := r.List()
	if err != nil {
		return fmt.Errorf("listing skills for scan: %w", err)
	}
	var errs []error
	for _, sk := range skills {
		if !r.IsEnabled(sk.Name) {
			continue
		}
		target := sk.Path
		if target == "" {
			target = filepath.Join(r.dir, sk.Name)
		}
		if r.reporter != nil {
			if fresh, _ := r.reporter.HasFreshSecCheckReport(target, stale); fresh {
				log.Printf("[skillbridge] seccheck: %q has a fresh report, skipping", sk.Name)
				continue
			}
		}
		result, err := r.ScanSkill(ctx, sk, target)
		if err != nil {
			// Visible, not silent: the gate is armed but cannot scan.
			log.Printf("[skillbridge] seccheck: %q NOT scanned: %v", sk.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", sk.Name, err))
			continue
		}
		log.Printf("[skillbridge] seccheck: %q verdict=%s findings=%d", sk.Name, result.Verdict, len(result.Findings))
	}
	return errors.Join(errs...)
}

// persistReport writes a scan result to the seccheck_reports table if a
// reporter is configured. A persistence failure is logged but never blocks the
// scan result itself (the result is returned to the caller regardless).
func (r *Registry) persistReport(_ context.Context, skillID, target string, result *honeybadger.ScanResult) {
	if r.reporter == nil {
		return
	}
	reportJSON, err := json.Marshal(result)
	if err != nil {
		log.Printf("[skillbridge] WARNING: failed to encode seccheck report for %q: %v", skillID, err)
		return
	}
	score := result.Score()
	if err := r.reporter.SaveSecCheckReport(skillID, target, "", score, result.Verdict, result.Reasoning, string(reportJSON)); err != nil {
		log.Printf("[skillbridge] WARNING: failed to persist seccheck report for %q: %v", skillID, err)
		return
	}
	log.Printf("[skillbridge] seccheck report saved: %q verdict=%s score=%d", skillID, result.Verdict, score)
}

// resolveScanTarget decides whether an install reference is a local path or a
// remote repository (URL or org/repo shorthand), and normalises it. Online
// scans keep network checks; local scans run offline (deterministic, no auth).
func resolveScanTarget(nameOrPath string) (target string, online bool) {
	if strings.Contains(nameOrPath, "://") || strings.HasPrefix(nameOrPath, "git@") {
		return nameOrPath, true
	}
	if _, err := os.Stat(nameOrPath); err == nil {
		return nameOrPath, false
	}
	// Org/repo shorthand (e.g. "famclaw/seccheck") — expand to a GitHub URL.
	if strings.Count(nameOrPath, "/") == 1 && !strings.HasPrefix(nameOrPath, ".") {
		return "https://github.com/" + nameOrPath, true
	}
	return nameOrPath, false
}
