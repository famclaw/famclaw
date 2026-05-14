package browser

import "github.com/famclaw/famclaw/internal/agentcore"

// Tools returns the atomic browser_* builtin tools the LLM can call.
//
// The workflow the model is expected to follow:
//   1. browser_navigate(url) — opens a page; returns a snapshot of interactive
//      elements with ref ids (e1, e2, …).
//   2. Look at the snapshot. Identify the element you want by its role+name.
//   3. browser_fill(ref=eN, value=...) / browser_click(ref=eN) / browser_select / etc.
//      Every action returns a FRESH snapshot so refs are always current.
//   4. browser_wait_for(ref=eN) when content loads after a click.
//   5. browser_extract(mode="text" [, ref=eN]) to read scraped content.
//
// REFS ARE EPHEMERAL: ref ids are valid only against the most recent
// snapshot. They are renumbered after every action. Always use the refs
// from the LAST tool reply.
func Tools(allowedRoles []string) []agentcore.Tool {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	intp := func(desc string) map[string]any {
		return map[string]any{"type": "integer", "description": desc}
	}
	boolp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	mk := func(name, desc string, props map[string]any, required []string) agentcore.Tool {
		return agentcore.Tool{
			Name:        "builtin__browser_" + name,
			Source:      "builtin",
			Description: desc,
			Roles:       allowedRoles,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": props,
				"required":   normRequired(required),
			},
		}
	}

	return []agentcore.Tool{
		mk("navigate",
			"Open a URL in your browser tab. Use this FIRST when researching anything that needs interaction. Returns a snapshot of interactive elements on the page with ref ids you can pass to other browser_* tools.",
			map[string]any{
				"url":        str("Absolute http(s) URL to navigate to."),
				"timeout_ms": intp("Optional navigation timeout in ms (default 20000)."),
			},
			[]string{"url"},
		),
		mk("snapshot",
			"Re-snapshot the current page. Use this when you need a fresh ref table (e.g. after content has loaded dynamically). Returns the same shape as browser_navigate's reply.",
			map[string]any{},
			nil,
		),
		mk("click",
			"Click an element by its ref id (e.g. e3) from the latest snapshot. Returns the page snapshot after the click — content may have changed.",
			map[string]any{
				"ref":        str("Ref id from the latest snapshot, e.g. \"e3\"."),
				"timeout_ms": intp("Optional wait timeout in ms (default 8000)."),
			},
			[]string{"ref"},
		),
		mk("fill",
			"Type a value into a textbox/searchbox/combobox identified by its ref id. Returns a fresh snapshot.",
			map[string]any{
				"ref":        str("Ref id of a textbox/searchbox/combobox element."),
				"value":      str("Text to type into the field."),
				"timeout_ms": intp("Optional wait timeout in ms (default 8000)."),
			},
			[]string{"ref", "value"},
		),
		mk("select",
			"Choose an option in a <select> dropdown by its value attribute. The ref must point at a listbox/combobox element.",
			map[string]any{
				"ref":        str("Ref id of a <select>/listbox element."),
				"value":      str("The option value to select."),
				"timeout_ms": intp("Optional wait timeout in ms (default 8000)."),
			},
			[]string{"ref", "value"},
		),
		mk("press_key",
			"Press a single keyboard key globally on the page. Useful for submitting forms (Enter), tabbing (Tab), or arrow keys. Examples: Enter, Tab, ArrowDown, Escape.",
			map[string]any{
				"key": str("Key name per Playwright (Enter, Tab, ArrowDown, etc.)."),
			},
			[]string{"key"},
		),
		mk("wait_for",
			"Block until the element with this ref becomes visible. Use after click/fill when the page needs time to render new content. Returns a fresh snapshot once the element appears.",
			map[string]any{
				"ref":        str("Ref id from the latest snapshot."),
				"timeout_ms": intp("Optional max wait in ms (default 12000)."),
			},
			[]string{"ref"},
		),
		mk("extract",
			"Read text or HTML from the page. mode=text returns visible text of the element (or whole body if no ref). mode=html returns inner HTML. Use this LAST, after the page shows the data you want. Output is truncated at 8000 chars.",
			map[string]any{
				"mode": str("One of: text | html."),
				"ref":  str("Optional ref id to scope the extraction. Omit to read the whole body."),
			},
			[]string{"mode"},
		),
		mk("screenshot",
			"Capture a PNG screenshot of the current page. Returns size info only (binary cannot render to chat). Useful for diagnostics.",
			map[string]any{
				"full_page": boolp("If true, capture full scrollable page; else viewport only. Default false."),
			},
			nil,
		),
		mk("fill_form", // builtin__browser_fill_form
			"Fill multiple form fields in one call. ALWAYS prefer this tool over multiple individual browser_fill or browser_click calls when interacting with forms — it is significantly faster, more reliable, and reduces turn count.",
			map[string]any{
				"fields": map[string]any{
					"type":        "array",
					"description": "Array of {ref, value} pairs to fill",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"ref":   str("Ref id from the latest snapshot, e.g. \"e3\"."),
							"value": str("Text to type into the field."),
						},
						"required": []string{"ref", "value"},
					},
				},
				"timeout_ms": intp("Optional per-field wait timeout in ms (default 8000)."),
			},
			[]string{"fields"},
		),
		mk("done", // builtin__browser_done
			"Signal that you have the answer for the user and the tool loop should terminate. Pass your final answer as summary. After calling this you MUST NOT call any more tools — emit a final text response (it can mirror the summary) on the next turn.",
			map[string]any{
				"summary": str("The final answer for the user."),
			},
			[]string{"summary"},
		),
	}
}

// normRequired ensures the "required" JSON-schema field is always an array,
// never null. llama-server's grammar/template parser rejects requests with
// "required": null (returns 400 "type must be array, but is null").
func normRequired(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
