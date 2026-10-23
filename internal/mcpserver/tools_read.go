package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
)

// handleStatus implements device.status. A downed device is reported as
// state "down" (not an error) so the butler can wait or report
// (design §5.3); only hard adb failures surface as errors.
func handleStatus(ctx context.Context, adb *ADBExecutor) (*mcp.CallToolResult, error) {
	st, err := adb.DeviceStatus(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("device.status failed: %v", err)), nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("encoding device status: %w", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}

// handleScreenshot implements screenshot: PNG bytes from screencap become
// an MCP ImageContent (base64) alongside a short caption (design §3.3).
func handleScreenshot(ctx context.Context, adb *ADBExecutor) (*mcp.CallToolResult, error) {
	png, err := adb.Screenshot(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("screenshot failed: %v", err)), nil
	}
	b64 := base64.StdEncoding.EncodeToString(png)
	return mcp.NewToolResultImage(
		fmt.Sprintf("Screenshot: %d bytes of PNG.", len(png)),
		b64, "image/png"), nil
}

// handleFind implements find: parse the (cached) uiautomator dump and
// return matching elements as a compact JSON array.
func handleFind(ctx context.Context, adb *ADBExecutor, args map[string]any) (*mcp.CallToolResult, error) {
	f := findFilters{
		Text: argString(args, "text"),
		Desc: argString(args, "content_desc"),
		ID:   argString(args, "resource_id"),
	}
	dump, err := adb.UIDump(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("find failed: %v", err)), nil
	}
	h, err := parseUIDump(dump)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("find failed: %v", err)), nil
	}
	matches := h.Find(f)
	if len(matches) == 0 {
		return mcp.NewToolResultText(
			"No elements matched. Try a broader text/content_desc/resource_id " +
				"filter, or fall back to screenshot reasoning."), nil
	}
	out, err := renderFindResults(matches)
	if err != nil {
		return nil, err
	}
	if len(matches) == maxFindResults {
		out += " (showing first " + strconv.Itoa(maxFindResults) +
			" matches; refine your filters for the rest)"
	}
	return mcp.NewToolResultText(string(out)), nil
}

// argString pulls a string argument from a tool-arguments map, tolerating
// missing keys and non-string values.
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}
