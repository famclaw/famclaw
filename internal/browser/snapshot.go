// Package browser drives a remote Playwright server to give famclaw real
// browser navigation. Refs assigned to interactive elements are stable across
// snapshots of the same page: each element is keyed by (role, name, nth) so
// the same DOM node keeps its ref id between turns. Elements that are new
// since the previous snapshot are rendered with a leading "*" in the formatted
// output (e.g. "*e7") so the model can spot autocomplete dropdowns and freshly
// rendered modals at a glance.
package browser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// interactiveRoles are the aria roles we surface to the LLM as refable
// elements. Anything else in the snapshot (headings, lists, paragraphs)
// is shown as context but not assigned a ref.
var interactiveRoles = map[string]bool{
	"button":           true,
	"link":             true,
	"textbox":          true,
	"searchbox":        true,
	"combobox":         true,
	"checkbox":         true,
	"radio":            true,
	"listbox":          true,
	"option":           true,
	"menuitem":         true,
	"menuitemcheckbox": true,
	"menuitemradio":    true,
	"slider":           true,
	"spinbutton":       true,
	"switch":           true,
	"tab":              true,
	"treeitem":         true,
}

// snapshotLineRE matches one line of Playwright AriaSnapshot YAML output.
// Examples it matches:
//   - button "Search"
//   - textbox "Where from?" [disabled]
//   - heading "title" [level=1]
//   - listitem: one
//   - link "Sign in":
//
// Captures:
//   1: leading whitespace (indent)
//   2: role (e.g. "button", may include "/" prefixed pseudo-keys we ignore)
//   3: optional name in quotes
//   4: optional [attrs]
//   5: optional trailing value after ":"
var snapshotLineRE = regexp.MustCompile(`^([ \t]*)-\s+(/?[A-Za-z][\w-]*)(?:\s+"([^"]*)")?(?:\s+\[([^\]]+)\])?(?::\s*(.*?))?\s*$`)

// RefEntry is the resolution data we store per ref so we can rebuild a
// Locator on demand. (Role, Name, Nth) is what we feed to page.GetByRole.
type RefEntry struct {
	Role string
	Name string
	Nth  int // 0-based index among same (Role, Name) on the page
}

// buildSnapshot calls AriaSnapshot on the page body, parses the result, and
// assigns refs (e1, e2, …) to interactive roles. prevRefs maps the stable
// (role, name, nth) triple to the ref id assigned in the previous snapshot.
// Elements whose triple appears in prevRefs keep their old ref id unchanged.
// Elements that are new (not in prevRefs) receive a fresh sequential id AND
// their rendered line is prefixed with "*" so the model can spot new content.
//
// newPrevRefs contains the triples → ids for exactly the elements present in
// this snapshot; the caller stores it for the next call. Elements that
// disappeared are not carried forward (garbage-collected).
func buildSnapshot(page playwright.Page, prevRefs map[string]string) (formatted string, refs map[string]RefEntry, newPrevRefs map[string]string, err error) {
	body := page.Locator("body")
	raw, err := body.AriaSnapshot()
	if err != nil {
		return "", nil, nil, fmt.Errorf("AriaSnapshot: %w", err)
	}

	// Compute the next available sequential number from the previous ref ids
	// so that reused refs keep their numbers and new elements always get a
	// number beyond anything previously assigned.
	nextSeq := 1
	for _, refID := range prevRefs {
		var n int
		if _, e := fmt.Sscanf(refID, "e%d", &n); e == nil && n >= nextSeq {
			nextSeq = n + 1
		}
	}

	type counted struct {
		count int
	}
	roleNameCount := make(map[string]*counted)
	refs = make(map[string]RefEntry)
	newPrevRefs = make(map[string]string)

	var b strings.Builder

	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := snapshotLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := m[1]
		role := m[2]
		name := m[3]
		attrs := m[4]
		value := m[5]

		// Skip aria pseudo-keys ("/children", "/url", etc.) — they are
		// structural data, not actionable.
		if strings.HasPrefix(role, "/") {
			continue
		}

		var prefix string
		if interactiveRoles[role] {
			// Compute the nth occurrence of this (role, name) pair so far.
			rnKey := role + "\x00" + name
			c, ok := roleNameCount[rnKey]
			if !ok {
				c = &counted{}
				roleNameCount[rnKey] = c
			}
			nth := c.count
			c.count++

			refKey := role + "\x00" + name + "\x00" + strconv.Itoa(nth)

			var ref string
			var isNew bool
			if existing, seen := prevRefs[refKey]; seen {
				ref = existing
			} else {
				ref = fmt.Sprintf("e%d", nextSeq)
				nextSeq++
				isNew = true
			}

			refs[ref] = RefEntry{Role: role, Name: name, Nth: nth}
			newPrevRefs[refKey] = ref

			if isNew {
				prefix = "*" + ref + "  "
			} else {
				prefix = ref + "  "
			}
		} else {
			prefix = "    "
		}

		// Emit a compact line for the LLM. Keep indent so the model can
		// see structural grouping.
		fmt.Fprintf(&b, "%s%s%s", indent, prefix, role)
		if name != "" {
			fmt.Fprintf(&b, " %q", name)
		}
		if attrs != "" {
			fmt.Fprintf(&b, " [%s]", attrs)
		}
		if value != "" {
			fmt.Fprintf(&b, ": %s", value)
		}
		b.WriteByte(0x0a)
	}

	return b.String(), refs, newPrevRefs, nil
}

// resolveRef returns a Locator usable for click/fill/etc. based on the
// stored (Role, Name, Nth) tuple. Returns an error if the ref does not
// exist in the session's current ref table.
func resolveRef(page playwright.Page, refs map[string]RefEntry, ref string) (playwright.Locator, error) {
	e, ok := refs[ref]
	if !ok {
		return nil, fmt.Errorf("ref %q not found in current snapshot — call browser_snapshot to refresh", ref)
	}
	role := playwright.AriaRole(e.Role)
	opts := playwright.PageGetByRoleOptions{}
	if e.Name != "" {
		opts.Name = e.Name
	}
	loc := page.GetByRole(role, opts)
	if e.Nth > 0 {
		loc = loc.Nth(e.Nth)
	}
	return loc, nil
}
