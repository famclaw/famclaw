// Package filetool provides builtin file tools for the agent.
package filetool

import (
	"github.com/famclaw/famclaw/internal/agentcore"
)

// FileReadTool returns the tool definition for builtin__file_read.
func FileReadTool() agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__file_read",
		Source:      "builtin",
		Description: "Read the contents of a file. The file must be within the configured sandbox root.",
		Roles:       []string{"parent", "child"}, // Policy will restrict children
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read, relative to the sandbox root.",
				},
			},
			"required": []string{"path"},
		},
	}
}

// FileWriteTool returns the tool definition for builtin__file_write.
func FileWriteTool() agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__file_write",
		Source:      "builtin",
		Description: "Write content to a file. The file must be within the configured sandbox root. Creates the file if it does not exist, overwrites if it does.",
		Roles:       []string{"parent", "child"}, // Policy will restrict children
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write, relative to the sandbox root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Content to write to the file.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

// FileStatTool returns the tool definition for builtin__file_stat.
func FileStatTool() agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__file_stat",
		Source:      "builtin",
		Description: "Retrieve file information (size, mode, modification time) for a file within the sandbox root.",
		Roles:       []string{"parent", "child"}, // Policy will restrict children
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to stat, relative to the sandbox root.",
				},
			},
			"required": []string{"path"},
		},
	}
}

// FileListTool returns the tool definition for builtin__file_list.
func FileListTool() agentcore.Tool {
	return agentcore.Tool{
		Name:        "builtin__file_list",
		Source:      "builtin",
		Description: "List files and directories in a given path within the sandbox root.",
		Roles:       []string{"parent", "child"}, // Policy will restrict children
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path to list, relative to the sandbox root. Empty string lists the sandbox root itself.",
				},
			},
			"required": []string{"path"},
		},
	}
}
