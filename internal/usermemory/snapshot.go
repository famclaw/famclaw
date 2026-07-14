package usermemory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Snapshot is a read-only view of a user's memories for prompt injection.
// Built by AlwaysInjectedSnapshot. A nil Snapshot is treated as empty.
type Snapshot struct {
	// MemoriesByCategory maps category name -> memories for that category.
	MemoriesByCategory map[string][]Memory

	// unavailable is set by UnavailableSnapshot() and renders a notice.
	unavailable bool
}

// UnavailableSnapshot returns the sentinel used when the snapshot DB read fails.
// memoryComponent renders this as a system-level notice.
func UnavailableSnapshot() *Snapshot {
	return &Snapshot{unavailable: true}
}

// IsEmpty reports whether the snapshot would render to nothing.
// The unavailable sentinel is NOT empty (it has a notice to render).
func (s *Snapshot) IsEmpty() bool {
	if s == nil {
		return true
	}
	if s.unavailable {
		return false
	}
	for _, rows := range s.MemoriesByCategory {
		if len(rows) > 0 {
			return false
		}
	}
	return true
}

const unavailableNotice = "<user_memory>\nmemory context temporarily unavailable — operating without personal memory\n</user_memory>"

// Render produces the system-prompt-ready block. Opening and closing
// <user_memory> tags are always present when Render returns a non-empty string.
// Ordering is deterministic: categories alpha, labels alpha within each.
func (s *Snapshot) Render() string {
	if s == nil {
		return ""
	}
	if s.unavailable {
		return unavailableNotice
	}
	if s.IsEmpty() {
		return ""
	}

	cats := make([]string, 0, len(s.MemoriesByCategory))
	for c := range s.MemoriesByCategory {
		if len(s.MemoriesByCategory[c]) > 0 {
			cats = append(cats, c)
		}
	}
	sort.Strings(cats)

	var b strings.Builder
	b.WriteString("<user_memory>\n")
	for _, c := range cats {
		rows := s.MemoriesByCategory[c]
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].Label < rows[j].Label
		})

		label := categoryDisplayLabel(c)
		fmt.Fprintf(&b, "- %s:", label)
		for i, m := range rows {
			sep := " "
			if i > 0 {
				sep = ". "
			}
			fmt.Fprintf(&b, "%s%s — %s", sep, m.Label, m.Value)
		}
		b.WriteString(".\n")
	}
	b.WriteString("</user_memory>")
	return b.String()
}

// categoryDisplayLabel makes "preferences" -> "Preferences",
// "ongoing_context" -> "Ongoing context".
func categoryDisplayLabel(name string) string {
	if name == "" {
		return name
	}
	words := strings.Split(name, "_")
	if len(words[0]) > 0 {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}

// AlwaysInjectedSnapshot reads all memories for a specific user.
// Orphan rows (unknown user) are excluded and logged via slog.Warn.
func (s *Store) AlwaysInjectedSnapshot(ctx context.Context, userName string) (*Snapshot, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, user_name, category, label, value, created_at, updated_at
		FROM user_memories WHERE user_name = ?
		ORDER BY category, label`, userName)
	if err != nil {
		return nil, fmt.Errorf("snapshot query: %w", err)
	}
	defer rows.Close()

	out := &Snapshot{MemoriesByCategory: make(map[string][]Memory)}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("snapshot scan: %w", err)
		}
		if m.UserName != userName {
			slog.Warn("user_memories: user_name mismatch, skipping",
				"expected", userName, "got", m.UserName, "id", m.ID)
			continue
		}
		out.MemoriesByCategory[m.Category] = append(out.MemoriesByCategory[m.Category], m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot iterate: %w", err)
	}
	return out, nil
}