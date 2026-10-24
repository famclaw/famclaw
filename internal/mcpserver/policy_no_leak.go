package mcpserver

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// SanitizeResult redacts any occurrence of the configured adb serial from a
// CallToolResult (design §5.7: serial must never appear in tool results,
// errors, or logs). It is the second layer behind the executor's own
// redaction — belt and braces.
func (pe *PolicyEngine) SanitizeResult(res *mcp.CallToolResult) *mcp.CallToolResult {
	if res == nil || pe.cfg.DeviceSerial == "" || len(res.Content) == 0 {
		return res
	}
	out := *res
	out.Content = make([]mcp.Content, len(res.Content))
	copy(out.Content, res.Content)
	for i := range out.Content {
		if tc, ok := out.Content[i].(mcp.TextContent); ok {
			tc.Text = strings.ReplaceAll(tc.Text, pe.cfg.DeviceSerial, "[SERIAL]")
			out.Content[i] = tc
		}
	}
	return &out
}

// sanitizeError returns a serial-redacted copy of err's message, for
// errors that flow back to the MCP result text.
func (pe *PolicyEngine) sanitizeError(err error) error {
	if err == nil || pe.cfg.DeviceSerial == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), pe.cfg.DeviceSerial, "[SERIAL]"))
}
