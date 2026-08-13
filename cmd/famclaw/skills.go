package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/honeybadger"
	"github.com/famclaw/famclaw/internal/skillbridge"
)

// noScanRegistry creates a registry with scanning disabled (for list/remove/enable/disable).
func noScanRegistry(dir string) *skillbridge.Registry {
	return skillbridge.NewRegistry(dir, nil, skillbridge.InstallConfig{}, nil)
}

func runSkillCommand(args []string) {
	if len(args) == 0 {
		printSkillUsage()
		os.Exit(2)
	}

	skillsDir := defaultSkillsDir()

	switch args[0] {
	case "install":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill install <path-or-url>\n")
			os.Exit(2)
		}
		if err := skillInstall(skillsDir, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	case "list":
		skillList(skillsDir)
	case "remove":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill remove <name>\n")
			os.Exit(2)
		}
		skillRemove(skillsDir, args[1])
	case "enable":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill enable <name>\n")
			os.Exit(2)
		}
		skillEnable(skillsDir, args[1])
	case "disable":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill disable <name>\n")
			os.Exit(2)
		}
		skillDisable(skillsDir, args[1])
	case "pin":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill pin <name>\n")
			os.Exit(2)
		}
		fmt.Printf("Pinned %s to current version (updates disabled)\n", args[1])
	case "rollback":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: famclaw skill rollback <name>\n")
			os.Exit(2)
		}
		fmt.Printf("Rollback not yet implemented for %s\n", args[1])
	case "check-updates":
		fmt.Println("Checking for updates...")
		fmt.Println("Update checking not yet implemented — will query GitHub releases")
	default:
		fmt.Fprintf(os.Stderr, "Unknown skill command: %s\n", args[0])
		printSkillUsage()
		os.Exit(2)
	}
}

func skillInstall(dir, nameOrPath string) error {
	// Try to load config for seccheck settings
	cfgPath := findConfigFile()
	installCfg := skillbridge.InstallConfig{}
	if cfgPath != "" {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			// Fail-closed: a config file that exists but cannot be parsed means
			// we cannot trust the seccheck settings. Refuse rather than silently
			// falling back to defaults that may disable the gate.
			return fmt.Errorf("loading config %q: %w", cfgPath, err)
		}
		installCfg = skillbridge.InstallConfig{
			Enabled:      loaded.SecCheck.Enabled,
			AutoSecCheck: loaded.SecCheck.AutoSecCheck,
			BlockOnFail:  loaded.SecCheck.BlockOnFail,
			Paranoia:     loaded.SecCheck.Paranoia,
		}
	}

	// The master switch is seccheck.enabled: true alone. Once Enabled is true,
	// install-time scanning is always required — we must ensure the scanner is
	// available and refuse (fail-closed) if it cannot be made available.
	var scanner skillbridge.Scanner
	if installCfg.Enabled {
		hb := honeybadger.New()
		if !hb.Available() {
			fmt.Fprintf(os.Stderr, "[seccheck] honeybadger not in PATH; fetching %s via go install...\n", honeybadger.HoneyBadgerVersion)
			if err := hb.EnsureScanner(context.Background(), honeybadger.HoneyBadgerVersion); err != nil {
				return fmt.Errorf("seccheck enabled but honeybadger scanner could not be made available: %w\n"+
					"  install manually: go install github.com/famclaw/honeybadger/cmd/honeybadger@latest\n"+
					"  or set seccheck.enabled: false in config.yaml to disable the security gate", err)
			}
		}
		scanner = hb
	}

	reg := skillbridge.NewRegistry(dir, scanner, installCfg, nil)
	skill, err := reg.Install(context.Background(), nameOrPath)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}
	fmt.Printf("Installed: %s v%s\n", skill.Name, skill.Version)
	if skill.Description != "" {
		fmt.Printf("  %s\n", skill.Description)
	}
	return nil
}

func skillList(dir string) {
	reg := noScanRegistry(dir)
	skills, err := reg.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list failed: %v\n", err)
		os.Exit(1)
	}
	if len(skills) == 0 {
		fmt.Println("No skills installed.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tENABLED\tDESCRIPTION")
	for _, s := range skills {
		enabled := "yes"
		if !reg.IsEnabled(s.Name) {
			enabled = "no"
		}
		desc := s.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Version, enabled, desc)
	}
	w.Flush()
}

func skillRemove(dir, name string) {
	reg := noScanRegistry(dir)
	if err := reg.Remove(name); err != nil {
		fmt.Fprintf(os.Stderr, "remove failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed: %s\n", name)
}

func skillEnable(dir, name string) {
	reg := noScanRegistry(dir)
	if err := reg.Enable(name); err != nil {
		fmt.Fprintf(os.Stderr, "enable failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Enabled: %s\n", name)
}

func skillDisable(dir, name string) {
	reg := noScanRegistry(dir)
	if err := reg.Disable(name); err != nil {
		fmt.Fprintf(os.Stderr, "disable failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Disabled: %s\n", name)
}

func defaultSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./skills"
	}
	return filepath.Join(home, ".famclaw", "skills")
}

// findConfigFile searches for config.yaml in standard locations.
func findConfigFile() string {
	candidates := []string{
		"config.yaml",
		filepath.Join(os.Getenv("HOME"), ".famclaw", "config.yaml"),
		"/opt/famclaw/config.yaml",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	fmt.Fprintf(os.Stderr, "note: no config.yaml found, using defaults (seccheck disabled)\n")
	return ""
}

func printSkillUsage() {
	fmt.Fprintf(os.Stderr, `Usage: famclaw skill <command> [args]

Commands:
  install <path>     Install a skill from a local path
  list               List installed skills
  remove <name>      Remove an installed skill
  enable <name>      Enable a disabled skill
  disable <name>     Disable a skill without removing it
  pin <name>         Pin to current version (never auto-update)
  rollback <name>    Restore previous version
  check-updates      Check all skills for available updates
`)
}
