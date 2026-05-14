package familystate

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// Snapshot is a read-only view of always-inject family facts, scoped
// to the families' valid subjects. Built by AlwaysInjectedSnapshot.
// A nil Snapshot is treated as empty by callers.
type Snapshot struct {
	// InjectedByCategory maps category name → facts for that category.
	// Keys are categories whose always_inject column is 1.
	InjectedByCategory map[string][]Fact

	// unavailable is set by UnavailableSnapshot() and renders a
	// "safety context temporarily unavailable" notice instead of facts.
	unavailable bool
}

// UnavailableSnapshot returns the sentinel value used when the snapshot
// DB read fails. memoryComponent renders this as a system-level notice
// so the model knows it is operating without safety context (R3 council
// 2-branch fail-stance).
func UnavailableSnapshot() *Snapshot {
	return &Snapshot{unavailable: true}
}

// IsEmpty reports whether the snapshot would render to nothing. The
// unavailable sentinel is NOT empty (it has a notice to render).
func (s *Snapshot) IsEmpty() bool {
	if s == nil {
		return true
	}
	if s.unavailable {
		return false
	}
	for _, rows := range s.InjectedByCategory {
		if len(rows) > 0 {
			return false
		}
	}
	return true
}

const unavailableNotice = "<family_safety>\nsafety context temporarily unavailable — operating with reduced family context\n</family_safety>"

// Render produces the system-prompt-ready block. The opening and
// closing <family_safety> tags are always present when Render returns
// a non-empty string. Ordering is deterministic: categories alpha,
// subjects alpha within each (with 'family' first), labels alpha
// within each subject. Snapshot tests pin this output exactly.
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

	cats := make([]string, 0, len(s.InjectedByCategory))
	for c := range s.InjectedByCategory {
		if len(s.InjectedByCategory[c]) == 0 {
			continue
		}
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var b strings.Builder
	b.WriteString("<family_safety>\n")
	for _, c := range cats {
		rows := s.InjectedByCategory[c]
		// Sort: family first, then subjects alpha, then labels alpha within each subject.
		sort.SliceStable(rows, func(i, j int) bool {
			si, sj := rows[i].Subject, rows[j].Subject
			if si != sj {
				if si == "family" {
					return true
				}
				if sj == "family" {
					return false
				}
				return si < sj
			}
			return rows[i].Label < rows[j].Label
		})

		label := categoryDisplayLabel(c)
		fmt.Fprintf(&b, "- %s:", label)
		for i, f := range rows {
			sep := " "
			if i > 0 {
				sep = ". "
			}
			fmt.Fprintf(&b, "%s%s — %s (%s)", sep, displaySubject(f.Subject), f.Label, f.Value)
		}
		b.WriteString(".\n")
	}
	b.WriteString("</family_safety>")
	return b.String()
}

// categoryDisplayLabel makes "allergies" → "Allergies",
// "dietary_restrictions" → "Dietary restrictions".
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

// displaySubject capitalizes the first letter for prompt readability.
// "family" → "Family", "teo" → "Teo".
func displaySubject(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// AlwaysInjectedSnapshot reads every fact whose category has always_inject=1
// AND whose subject is in knownSubjects (i.e. validated against config.Users
// names ∪ {"family"}). Orphan rows (unknown subject) are excluded from the
// snapshot AND logged via slog.Warn so a renamed user does not silently
// produce misattributed context (R3 council drift lock).
func (s *Store) AlwaysInjectedSnapshot(ctx context.Context, knownSubjects map[string]bool) (*Snapshot, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT f.id, f.category, f.subject, f.label, f.value, f.recurrence, f.created_by, f.created_at, f.updated_at
		FROM family_facts f
		JOIN family_fact_categories c ON c.name = f.category
		WHERE c.always_inject = 1
		ORDER BY f.category, f.subject, f.label`)
	if err != nil {
		return nil, fmt.Errorf("snapshot query: %w", err)
	}
	defer rows.Close()

	out := &Snapshot{InjectedByCategory: make(map[string][]Fact)}
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, fmt.Errorf("snapshot scan: %w", err)
		}
		if !knownSubjects[f.Subject] {
			slog.Warn("family_facts: subject not in config, skipping",
				"subject", f.Subject, "category", f.Category, "id", f.ID)
			continue
		}
		out.InjectedByCategory[f.Category] = append(out.InjectedByCategory[f.Category], f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot iterate: %w", err)
	}
	return out, nil
}
