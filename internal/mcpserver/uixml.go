package mcpserver

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// maxFindResults caps find output so a busy screen can't flood the LLM
// context; the tool notes when the cap clipped results.
const maxFindResults = 80

// uiNode is one node of a uiautomator dump (the hierarchy is nested).
type uiNode struct {
	Text        string   `xml:"text,attr"`
	ResourceID  string   `xml:"resource-id,attr"`
	Class       string   `xml:"class,attr"`
	Package     string   `xml:"package,attr"`
	ContentDesc string   `xml:"content-desc,attr"`
	Enabled     bool     `xml:"enabled,attr"`
	Clickable   bool     `xml:"clickable,attr"`
	Scrollable  bool     `xml:"scrollable,attr"`
	Checked     bool     `xml:"checked,attr"`
	Selected    bool     `xml:"selected,attr"`
	Bounds      string   `xml:"bounds,attr"`
	Children    []uiNode `xml:"node"`
}

// uiHierarchy is the root of a uiautomator dump.
type uiHierarchy struct {
	XMLName xml.Name `xml:"hierarchy"`
	Nodes   []uiNode `xml:"node"`
}

// uiElement is one compact, LLM-friendly find output row: center
// coordinates plus the identifying fields (design §4 find row).
type uiElement struct {
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Class     string `json:"class,omitempty"`
	Text      string `json:"text,omitempty"`
	Desc      string `json:"desc,omitempty"`
	ID        string `json:"id,omitempty"`
	Package   string `json:"package,omitempty"`
	Clickable bool   `json:"clickable,omitempty"`
	Enabled   bool   `json:"enabled,omitempty"`
}

// findFilters are the find tool matchers; empty fields are wildcards.
type findFilters struct {
	Text string // matches uiElement.Text
	Desc string // matches uiElement.Desc (content-desc)
	ID   string // matches uiElement.ID (resource-id)
}

// parseUIDump parses a uiautomator dump into its node tree.
func parseUIDump(data string) (*uiHierarchy, error) {
	var h uiHierarchy
	if err := xml.Unmarshal([]byte(data), &h); err != nil {
		return nil, fmt.Errorf("parsing uiautomator dump: %w", err)
	}
	return &h, nil
}

// Find returns matching elements, capped at maxFindResults. All provided
// filters must match (AND); matching is case-insensitive substring. With
// no filters it returns elements carrying any label (text, desc, or
// resource-id) in document order — the compact "what's on screen" view.
func (h *uiHierarchy) Find(f findFilters) []uiElement {
	out := make([]uiElement, 0, 16)
	var walk func(nodes []uiNode)
	walk = func(nodes []uiNode) {
		for i := range nodes {
			if len(out) >= maxFindResults {
				return
			}
			n := nodes[i]
			el := toElement(n)
			if el != nil && matches(*el, f) {
				out = append(out, *el)
			}
			walk(n.Children)
		}
	}
	walk(h.Nodes)
	return out
}

// toElement converts a node to a compact element, or nil when the node has
// no parseable bounds or no identifying label.
func toElement(n uiNode) *uiElement {
	b, ok := parseBounds(n.Bounds)
	if !ok {
		return nil
	}
	if n.Text == "" && n.ContentDesc == "" && n.ResourceID == "" {
		return nil
	}
	return &uiElement{
		X:         (b.l + b.r) / 2,
		Y:         (b.t + b.b) / 2,
		Class:     n.Class,
		Text:      n.Text,
		Desc:      n.ContentDesc,
		ID:        n.ResourceID,
		Package:   n.Package,
		Clickable: n.Clickable,
		Enabled:   n.Enabled,
	}
}

// matches reports whether all provided filters hold (case-insensitive
// substring; empty filter matches everything).
func matches(el uiElement, f findFilters) bool {
	if f.Text != "" && !containsFold(el.Text, f.Text) {
		return false
	}
	if f.Desc != "" && !containsFold(el.Desc, f.Desc) {
		return false
	}
	if f.ID != "" && !containsFold(el.ID, f.ID) {
		return false
	}
	return true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// rect is an inclusive uiautomator bounds box "[l,t][r,b]".
type rect struct{ l, t, r, b int }

// parseBounds parses a uiautomator bounds attribute "[l,t][r,b]".
func parseBounds(s string) (rect, bool) {
	nums := make([]int, 0, 4)
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '[':
			depth++
		case ']':
			if depth > 0 && cur.Len() > 0 {
				n, err := strconv.Atoi(cur.String())
				if err != nil {
					return rect{}, false
				}
				nums = append(nums, n)
				cur.Reset()
			}
			depth--
		case ',':
			if depth > 0 {
				n, err := strconv.Atoi(cur.String())
				if err != nil {
					return rect{}, false
				}
				nums = append(nums, n)
				cur.Reset()
			}
		default:
			if depth > 0 {
				cur.WriteByte(c)
			}
		}
	}
	if len(nums) != 4 {
		return rect{}, false
	}
	return rect{nums[0], nums[1], nums[2], nums[3]}, true
}

// renderFindResults marshals matches as a compact JSON array.
func renderFindResults(matches []uiElement) (string, error) {
	data, err := json.Marshal(matches)
	if err != nil {
		return "", fmt.Errorf("encoding find results: %w", err)
	}
	return string(data), nil
}
