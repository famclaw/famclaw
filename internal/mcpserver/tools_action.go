package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// argInt pulls a non-negative integer from the tool-arguments map.
// Returns (0, false) when the key is absent, the value is not numeric,
// or the value is negative. Accepts float64 (JSON number), int, and json.Number.
func argInt(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch v := args[key].(type) {
	case float64:
		if v < 0 || v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case int64:
		if v < 0 {
			return 0, false
		}
		return int(v), true
	}
	return 0, false
}

// handleTap implements the tap tool (design §4).
//
// Selector-first: if any of text, content_desc, or resource_id is provided,
// the handler resolves the element via a fresh uiautomator dump and taps the
// first match's centre. Otherwise it falls back to explicit x/y coordinates.
// Neither form provided → soft error result telling the LLM what to supply.
func handleTap(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	text := argString(args, "text")
	desc := argString(args, "content_desc")
	id := argString(args, "resource_id")
	hasSelector := text != "" || desc != "" || id != ""

	var x, y int
	if hasSelector {
		dump, err := adb.UIDump(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("tap: could not read UI hierarchy: %v", err)), nil
		}
		h, err := parseUIDump(dump)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("tap: %v", err)), nil
		}
		matches := h.Find(findFilters{Text: text, Desc: desc, ID: id})
		if len(matches) == 0 {
			return mcp.NewToolResultText("No element matched the given selector. Take a screenshot or run find to re-locate the target."), nil
		}
		x, y = matches[0].X, matches[0].Y
	} else {
		var okX, okY bool
		x, okX = argInt(args, "x")
		y, okY = argInt(args, "y")
		if !okX || !okY {
			return mcp.NewToolResultText("tap requires either a selector (text / content_desc / resource_id) or explicit x and y coordinates."), nil
		}
	}

	if err := adb.InputTap(ctx, x, y); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("tap: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("tapped (%d, %d)", x, y)), nil
}

// handleType implements the type tool (design §4).
// The full text is checked against the TypePrecheck hook before any
// `input text` command is issued. The hook is the stage-4 seam for
// credential/OTP/passcode hard-stops (design §5.1).
func handleType(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	text := argString(args, "text")
	if text == "" {
		return mcp.NewToolResultText("type requires a non-empty text argument."), nil
	}

	// Pre-check hook (stage 4 will wire credential detection here).
	if adb.TypePrecheck != nil {
		if err := adb.TypePrecheck(text); err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Refused: %v", err)), nil
		}
	}

	if err := adb.InputText(ctx, text); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("type: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("typed %d character(s)", len([]rune(text)))), nil
}

// handleKeyEvent implements the keyevent tool (design §4).
// Valid keys: home (3), back (4), recents (187), menu (82).
func handleKeyEvent(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	key := strings.ToLower(argString(args, "key"))
	if key == "" {
		return mcp.NewToolResultText("keyevent requires a key argument (home, back, recents, menu)."), nil
	}
	if _, known := keyEventCodes[key]; !known {
		return mcp.NewToolResultText(fmt.Sprintf("unknown key %q; valid keys: home, back, recents, menu", key)), nil
	}

	if err := adb.KeyEvent(ctx, key); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("keyevent: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("sent keyevent %q", key)), nil
}

// handleSwipe implements the swipe tool (design §4).
// ms defaults to 300 when 0 or omitted.
func handleSwipe(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	x1, okX1 := argInt(args, "x1")
	y1, okY1 := argInt(args, "y1")
	x2, okX2 := argInt(args, "x2")
	y2, okY2 := argInt(args, "y2")
	if !okX1 || !okY1 || !okX2 || !okY2 {
		return mcp.NewToolResultText("swipe requires x1, y1, x2, y2."), nil
	}
	ms, _ := argInt(args, "ms") // 0 or omitted → executor applies default 300

	if err := adb.Swipe(ctx, x1, y1, x2, y2, ms); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("swipe: %v", err)), nil
	}
	reported := ms
	if reported <= 0 {
		reported = 300
	}
	return mcp.NewToolResultText(fmt.Sprintf("swiped (%d,%d)→(%d,%d) over %dms", x1, y1, x2, y2, reported)), nil
}

// handleOpenApp implements the open_app tool (design §4).
// Resolves the launcher activity via `cmd package resolve-activity` then
// launches it with `am start -W`.
func handleOpenApp(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	pkg := argString(args, "package")
	if pkg == "" {
		return mcp.NewToolResultText("open_app requires a package argument."), nil
	}

	comp, err := adb.ResolveLauncher(ctx, pkg)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("open_app: could not resolve a launchable activity for %s: %v", pkg, err)), nil
	}

	out, err := adb.StartApp(ctx, comp)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("open_app: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("opened %s via %s\n%s", pkg, comp, out)), nil
}

// handleShell implements the shell tool (design §4: operator-only, gated
// behind policy.Tools.Shell). The tool is only registered when the kill-switch
// is on; this handler is the last line of defence.
func handleShell(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	cmd := argString(args, "cmd")
	if cmd == "" {
		return mcp.NewToolResultText("shell requires a cmd argument."), nil
	}

	out, err := adb.Shell(ctx, cmd)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("shell: %v", err)), nil
	}
	return mcp.NewToolResultText(out), nil
}
