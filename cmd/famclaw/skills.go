package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/famclaw/famclaw/internal/skillbridge"
)

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
		skillInstall(skillsDir, args[1])
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown skill command: %s\n", args[0])
		printSkillUsage()
		os.Exit(2)
	}
}

func skillInstall(dir, nameOrPath string) {
	reg := skillbridge.NewRegistry(dir)
	skill, err := reg.Install(nameOrPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Installed: %s v%s\n", skill.Name, skill.Version)
	if skill.Description != "" {
		fmt.Printf("  %s\n", skill.Description)
	}
}

func skillList(dir string) {
	reg := skillbridge.NewRegistry(dir)
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
	reg := skillbridge.NewRegistry(dir)
	if err := reg.Remove(name); err != nil {
		fmt.Fprintf(os.Stderr, "remove failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed: %s\n", name)
}

func skillEnable(dir, name string) {
	reg := skillbridge.NewRegistry(dir)
	if err := reg.Enable(name); err != nil {
		fmt.Fprintf(os.Stderr, "enable failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Enabled: %s\n", name)
}

func skillDisable(dir, name string) {
	reg := skillbridge.NewRegistry(dir)
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

func printSkillUsage() {
	fmt.Fprintf(os.Stderr, `Usage: famclaw skill <command> [args]

Commands:
  install <path>   Install a skill from a local path
  list             List installed skills
  remove <name>    Remove an installed skill
  enable <name>    Enable a disabled skill
  disable <name>   Disable a skill without removing it
`)
}
