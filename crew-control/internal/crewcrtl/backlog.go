package crewcrtl

import (
	"fmt"
	"regexp"
	"strings"
)

// BacklogItem is a single entry from the firstmate backlog (data/backlog.md).
type BacklogItem struct {
	ID      string `json:"id"`                   // e.g. "fc-crew-control-mcp"
	Title   string `json:"title"`                // human-readable title text
	Repo    string `json:"repo,omitempty"`       // e.g. "famclaw"
	Kind    string `json:"kind,omitempty"`       // e.g. "ship", "scout"
	Since   string `json:"since,omitempty"`      // e.g. "2026-07-31"
	Blocked string `json:"blocked_by,omitempty"` // e.g. "fc-childapprove-t15"
	Raw     string `json:"raw"`                  // the original line for transparency
}

// Backlog is the parsed backlog, split by section.
type Backlog struct {
	InFlight []BacklogItem `json:"in_flight"`
	Queued   []BacklogItem `json:"queued"`
}

// backlogItemRe matches an UNCHECKED checklist line only.
// Checked ("- [x]") lines are intentionally NOT matched — they represent
// completed work and should not appear in the read-only in-flight/queued view.
//
// Example: "- [ ] fc-crew-control-mcp - ship: read-only... (repo: famclaw)"
// Captured groups: 1=crew ID, 2=rest of line (title + metadata).
var backlogItemRe = regexp.MustCompile(`^- \[ \] ([a-zA-Z0-9_-]+) - (.+)$`)

// metadataRe extracts parenthesised (key: value) pairs.
var metadataRe = regexp.MustCompile(`\((repo|kind|priority):([^)]*)\)`)

// sinceRe extracts a "since <date>" token matching YYYY-MM-DD.
var sinceRe = regexp.MustCompile(`since[:\s]+([0-9]{4}-[0-9]{2}-[0-9]{2})`)

// blockedByRe extracts a "blocked-by: <id>" token.
var blockedByRe = regexp.MustCompile(`blocked-by[:\s]+([a-zA-Z0-9_-]+)`)

// titleStripRe removes trailing metadata and parenthetical markers from the
// title to leave clean human-readable text.
var titleStripRe = regexp.MustCompile(`\s*\((repo|kind|priority):[^)]*\)|\s*blocked-by:\s*[^\s,)]+`)

// ParseBacklog parses the raw text of data/backlog.md into a Backlog.
// It extracts the "## In flight" and "## Queued" sections, ignoring
// "## Done" (read-only view of current work). Continuation lines that are
// indented under a checklist item are skipped — they are detail, not a new
// task.
func ParseBacklog(raw string) (*Backlog, error) {
	if raw == "" {
		return &Backlog{}, nil
	}

	var (
		bl           = &Backlog{}
		section      = ""
		lines        = strings.Split(raw, "\n")
		itemLineSeen = false // tracks whether we just saw a checklist line
	)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Section headers.
		if strings.HasPrefix(trimmed, "## ") {
			switch trimmed {
			case "## In flight":
				section = "in_flight"
			case "## Queued":
				section = "queued"
			default:
				section = "" // ## Done, etc. — not parsed
			}
			itemLineSeen = false
			continue
		}

		// Skip blank lines and non-checklist content when not in a target section.
		if section == "" {
			continue
		}

		match := backlogItemRe.FindStringSubmatch(trimmed)
		if match != nil {
			itemLineSeen = true
			item := parseBacklogItem(match[1], match[2])
			if section == "in_flight" {
				bl.InFlight = append(bl.InFlight, item)
			} else {
				bl.Queued = append(bl.Queued, item)
			}
		} else if itemLineSeen && strings.HasPrefix(line, "  ") {
			// Indented continuation line — detail of the previous item, skip.
			continue
		} else {
			itemLineSeen = false
		}
	}

	return bl, nil
}

// parseBacklogItem builds a BacklogItem from the crew ID and the rest of the
// checklist line (title + metadata).
func parseBacklogItem(id, rest string) BacklogItem {
	item := BacklogItem{
		ID:  id,
		Raw: rest,
	}

	// Extract metadata pairs: (repo: famclaw) (kind: ship)
	for _, m := range metadataRe.FindAllStringSubmatch(rest, -1) {
		key := m[1]
		val := strings.TrimSpace(m[2])
		switch key {
		case "repo":
			item.Repo = val
		case "kind":
			item.Kind = val
		}
	}

	// Extract "since <date>" or "since: <date>".
	if m := sinceRe.FindStringSubmatch(rest); m != nil {
		item.Since = m[1]
	}

	// Extract "blocked-by: <id>".
	if m := blockedByRe.FindStringSubmatch(rest); m != nil {
		item.Blocked = m[1]
	}

	// Clean title: strip metadata and blocked-by markers.
	item.Title = strings.TrimSpace(titleStripRe.ReplaceAllString(rest, ""))

	return item
}

// FormatMarkdown renders the backlog as a human-readable Markdown summary.
func (b *Backlog) FormatMarkdown() string {
	var sb strings.Builder

	sb.WriteString("## In Flight\n\n")
	if len(b.InFlight) == 0 {
		sb.WriteString("No in-flight backlog items.\n")
	} else {
		sb.WriteString(fmt.Sprintf("%d in-flight item(s):\n\n", len(b.InFlight)))
		for i, item := range b.InFlight {
			sb.WriteString(formatBacklogItem(i+1, item))
		}
	}

	sb.WriteString("\n## Queued\n\n")
	if len(b.Queued) == 0 {
		sb.WriteString("No queued backlog items.\n")
	} else {
		sb.WriteString(fmt.Sprintf("%d queued item(s):\n\n", len(b.Queued)))
		for i, item := range b.Queued {
			sb.WriteString(formatBacklogItem(i+1, item))
		}
	}

	return sb.String()
}

func formatBacklogItem(n int, item BacklogItem) string {
	var line strings.Builder
	fmt.Fprintf(&line, "%d. **%s** — %s\n", n, item.ID, item.Title)
	if item.Repo != "" || item.Kind != "" || item.Since != "" || item.Blocked != "" {
		line.WriteString("   - ")
		parts := []string{}
		if item.Repo != "" {
			parts = append(parts, "repo: "+item.Repo)
		}
		if item.Kind != "" {
			parts = append(parts, "kind: "+item.Kind)
		}
		if item.Since != "" {
			parts = append(parts, "since: "+item.Since)
		}
		if item.Blocked != "" {
			parts = append(parts, "blocked-by: "+item.Blocked)
		}
		line.WriteString(strings.Join(parts, ", "))
		line.WriteString("\n")
	}
	line.WriteString("\n")
	return line.String()
}
