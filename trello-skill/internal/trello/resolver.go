package trello

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// idRe matches a Trello object id: exactly 24 lowercase hexadecimal characters.
// Trello ids are 24-char hex; anything that is not one (e.g. "Backlog") is
// either a name to resolve or an error. This is the cheap pre-flight check
// that stops the skill from hitting the API with a garbage list_id.
var idRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// forRe matches a trailing "(For <name>)" / "(for <name>)" suffix on a card
// title. The skill strips this when <name> is a known list/person name, so the
// model stops burying the routing target in the title — routing is done by the
// `person` arg instead.
var forRe = regexp.MustCompile(`(?i)\s*\(\s*for\s+([A-Za-z][A-Za-z0-9 _]*)\s*\)\s*$`)

// Resolver translates human-friendly list/person names into Trello list ids and
// validates list_id arguments. It is the single source of truth for "which list
// is named X?", so the model can pass names like "Backlog" or "Julia" instead
// of raw ids (the thing that caused the duplicate-card spiral).
//
// The name→id map is populated from the TRELLO_LISTS env var (JSON), never
// hardcoded. Trello list ids are never embedded in source.
type Resolver struct {
	// Lists maps a list/person name to its Trello list id. Names are matched
	// case-insensitively. Populated from the TRELLO_LISTS env var.
	Lists map[string]string
	// DefaultListID is the list used when no target is specified. It is
	// TRELLO_LIST_ID, or Lists["Backlog"] when that env var is unset.
	DefaultListID string
	// DoneListID is the list completed cards are moved to. It is
	// TRELLO_DONE_LIST_ID, or Lists["Done"] when that env var is unset.
	DoneListID string
}

// NewResolver builds a Resolver from credentials.
func NewResolver(creds Credentials) *Resolver {
	r := &Resolver{
		Lists:         creds.Lists,
		DefaultListID: creds.ListID,
		DoneListID:    creds.DoneListID,
	}
	if r.DefaultListID == "" {
		r.DefaultListID = lookupName(r.Lists, "Backlog")
	}
	if r.DoneListID == "" {
		r.DoneListID = lookupName(r.Lists, "Done")
	}
	return r
}

// ResolveList resolves a list_id argument — which may be empty, a name, or a
// 24-char hex id — to a concrete Trello list id.
//
//   - ""                              -> the default list (TRELLO_LIST_ID/Backlog).
//   - a known name (case-insensitive) -> that list's id.
//   - a 24-char hex id present in the configured lists -> that id.
//   - a 24-char hex id NOT in the configured lists (when lists are configured)
//     -> error: the id is valid-format but unknown — this is exactly the "a
//     wrong id failed silently and invited retries" bug. The error names the
//     valid lists so the model can correct itself.
//   - anything else -> error: not a hex id and not a known list name.
//
// Returns the resolved id, the name it matched (for messages), and an error.
func (r *Resolver) ResolveList(arg string) (id, matched string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if r.DefaultListID == "" {
			return "", "", fmt.Errorf("no list id provided and no default list configured (set TRELLO_LIST_ID or TRELLO_LISTS['Backlog'])")
		}
		return r.DefaultListID, "default", nil
	}
	// Name match (case-insensitive).
	if id := lookupName(r.Lists, arg); id != "" {
		return id, findName(r.Lists, arg), nil
	}
	// 24-char hex id?
	if idRe.MatchString(arg) {
		if len(r.Lists) > 0 && !containsValue(r.Lists, arg) {
			return "", "", fmt.Errorf("list_id %q is a valid-format id but is not one of the configured lists; valid lists: %s", arg, r.formatValidLists())
		}
		return arg, arg, nil
	}
	// Not a known name and not a hex id.
	return "", "", fmt.Errorf("list_id %q is not a 24-char hex id and does not match a known list name; valid lists: %s", arg, r.formatValidLists())
}

// ResolvePerson resolves a trello_add_card `person` argument. This is the
// per-person routing tier.
//
//   - ""                          -> default list (the "neither" tier).
//   - a known name (case-insensitive) -> that person's list (explicit-target).
//   - a 24-char hex id that is a known list id -> that id.
//   - anything else               -> fall back to the default list. This is the
//     "falling back to TRELLO_LIST_ID when there is no mapping" tier: an
//     unmapped person is NOT a hard error, the card lands on the default list
//     instead. The returned fallback flag lets the handler report this clearly
//     (never silently).
func (r *Resolver) ResolvePerson(person string) (id, matchedName string, fallback bool, err error) {
	person = strings.TrimSpace(person)
	if person == "" {
		id, _, err := r.defaultList()
		return id, "default", false, err
	}
	if id := lookupName(r.Lists, person); id != "" {
		return id, findName(r.Lists, person), false, nil
	}
	if idRe.MatchString(person) && containsValue(r.Lists, person) {
		return person, person, false, nil
	}
	// Unmapped/unknown person -> fall back to the default list.
	id, _, derr := r.defaultList()
	return id, person, true, derr
}

// defaultList returns the configured default list id.
func (r *Resolver) defaultList() (string, string, error) {
	if r.DefaultListID == "" {
		return "", "", fmt.Errorf("no default list configured (set TRELLO_LIST_ID or TRELLO_LISTS['Backlog'])")
	}
	return r.DefaultListID, "default", nil
}

// formatValidLists returns a sorted "Name=id, Name=id" string for error and
// note messages.
func (r *Resolver) formatValidLists() string {
	if len(r.Lists) == 0 {
		return "(none configured: set skills.credentials.TRELLO_LISTS)"
	}
	names := sortedNames(r.Lists)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%s", n, r.Lists[n]))
	}
	return strings.Join(parts, ", ")
}

// cleanTitle strips a trailing "(For <name>)" suffix from the title when
// <name> is a known list/person name. This stops the model from burying the
// routing target in the title — the `person` arg is the real router.
func cleanTitle(title string, lists map[string]string) string {
	m := forRe.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	if lookupName(lists, m[1]) != "" {
		return strings.TrimSpace(title[:len(title)-len(m[0])])
	}
	return title
}

// sortedNames returns the names of a list map, sorted for stable output.
func sortedNames(lists map[string]string) []string {
	names := make([]string, 0, len(lists))
	for n := range lists {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// lookupName returns the id for name, matched case-insensitively against the
// map keys. Returns "" if not found or the map is empty.
func lookupName(lists map[string]string, name string) string {
	if len(lists) == 0 {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for k, v := range lists {
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return v
		}
	}
	return ""
}

// findName returns the actual stored key matching name (case-insensitive), so
// messages echo the canonical name.
func findName(lists map[string]string, name string) string {
	want := strings.ToLower(strings.TrimSpace(name))
	for k := range lists {
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return k
		}
	}
	return name
}

// containsValue reports whether want is a value in the map.
func containsValue(lists map[string]string, want string) bool {
	for _, v := range lists {
		if v == want {
			return true
		}
	}
	return false
}
