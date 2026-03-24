package skillbridge

import (
	"fmt"
	"strings"
)

// LoadForPrompt formats skills into AgentSkills XML for system prompt injection.
// This format is compatible with OpenClaw and PicoClaw.
func LoadForPrompt(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<AgentSkills>\n")
	for _, s := range skills {
		sb.WriteString(formatSkillXML(s))
	}
	sb.WriteString("</AgentSkills>")
	return sb.String()
}

func formatSkillXML(s *Skill) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  <Skill name=%q", s.Name))
	if s.Description != "" {
		sb.WriteString(fmt.Sprintf(" description=%q", s.Description))
	}
	if s.Version != "" {
		sb.WriteString(fmt.Sprintf(" version=%q", s.Version))
	}
	sb.WriteString(">\n")
	// Body injected verbatim — no modification
	for _, line := range strings.Split(s.Body, "\n") {
		sb.WriteString("    " + line + "\n")
	}
	sb.WriteString("  </Skill>\n")
	return sb.String()
}
