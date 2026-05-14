package browser

import (
	"fmt"
	"regexp"
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

// buildSnapshot calls AriaSnapshot on the page body, parses the result,
// assigns sequential refs (e1, e2, …) to interactive roles, and returns
// both the formatted LLM-facing string AND the ref→entry map for
// later locator resolution. Refs are stable only within the snapshot;
// callers must re-snapshot after any action that mutates the page.
func buildSnapshot(page playwright.Page) (formatted string, refs map[string]RefEntry, err error) {
	body := page.Locator("body")
	raw, err := body.AriaSnapshot()
	if err != nil {
		return "", nil, fmt.Errorf("AriaSnapshot: %w", err)
	}

	type counted struct {
		count int
	}
	roleNameCount := make(map[string]*counted)
	refs = make(map[string]RefEntry)

	var b strings.Builder
	var refSeq int

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
			refSeq++
			ref := fmt.Sprintf("e%d", refSeq)
			key := role + "\x00" + name
			c, ok := roleNameCount[key]
			if !ok {
				c = &counted{}
				roleNameCount[key] = c
			}
			refs[ref] = RefEntry{Role: role, Name: name, Nth: c.count}
			c.count++
			prefix = ref + "  "
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

	return b.String(), refs, nil
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
