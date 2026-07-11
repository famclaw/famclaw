package agent

import "testing"

func TestSanitizeModelResponse(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		output string
	}{
{
				name:   "thinking+final wrapper",
				input:  "```reasoning\nfinal answer```",
				output: "```reasoning\nfinal answer```",
			},
		{
			name:   "final wrapper only",
			input:  "<final>answer</final>",
			output: "answer",
		},
{
				name:   "thinking inside text",
				input:  "answer  thinkingnope continues",
				output: "answer  thinkingnope continues",
			},
		{
			name:   "no tags — pass through",
			input:  "hello world",
			output: "hello world",
		},
		{
			name:   "empty input",
			input:  "",
			output: "",
		},
		{
			name:   "only whitespace",
			input:  "   ",
			output: "",
		},
{
				name:   "nested thinking tags",
				input:  "```outer inner ###### final",
				output: "```outer inner ###### final",
			},
{
				name:   "malformed — opening tag only, no closing",
				input:  "thinking no close answer",
				output: "thinking no close answer",
			},
		{
			name:   "tool-call spill-over",
			input:  "hello  some tool output more",
			output: "hello  some tool output more",
		},
		{
			name:   "function block strip",
			input:  "before <function name=\"search\">query</function> after",
			output: "before  after",
		},
		{
			name:   "lowercase thinking tags",
			input:  "thought<thinking>reasoning</thinking>answer",
			output: "thoughtanswer",
		},
		{
			name:   "mixed case Final wrapper",
			input:  "reply <Final>result</Final> end",
			output: "reply result end",
		},
{
				name:   "multi-line thinking block",
				input:  "start\n<thinking>\nstep 1\nstep 2\n</thinking>\nfinal answer",
				output: "start\n\nfinal answer",
			},
		{
			name:   "multiple thinking blocks",
			input:  "before <thinking>first</thinking> middle <thinking>second</thinking> after",
			output: "before  middle  after",
		},
{
				name:   "final wrapper with multiline inner content",
				input:  "<final>\nline 1\nline 2\n</final>",
				output: "line 1\nline 2",
			},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeModelResponse(tt.input)
			if got != tt.output {
				t.Errorf("sanitizeModelResponse(%q) = %q, want %q", tt.input, got, tt.output)
			}
		})
	}
}
