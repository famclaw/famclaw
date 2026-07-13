package agent

import "testing"

func TestSanitizeModelResponse(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		output string
	}{
		{
			name:   "thinking plus final wrapper",
			input:  "<thinking>reasoning\nhere</thinking><final>final answer</final>",
			output: "final answer",
		},
		{
			name:   "final wrapper only",
			input:  "<final>answer</final>",
			output: "answer",
		},
		{
			name:   "thinking inside text",
			input:  "before <thinking>thought</thinking> after",
			output: "before  after",
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
			input:  "<thinking>outer <thinking>inner</thinking> tail</thinking>done",
			output: "taildone",
		},
		{
			name:   "nested final tags",
			input:  "<final>a<final>b</final>c</final>",
			output: "abc",
		},
		{
			name:   "deeply nested final tags",
			input:  "<Final><FINAL>core</FINAL></Final>",
			output: "core",
		},
		{
			name:   "four-level nested final tags",
			input:  "<final><final><final><final>core</final></final></final></final>",
			output: "core",
		},
		{
			name:   "four-level nested final with interleaved text",
			input:  "<final>a<final>b<final>c<final>d</final>e</final>f</final>g</final>",
			output: "abcdefg",
		},
		{
			name:   "malformed — opening tag only, no closing",
			input:  "thinking no close answer",
			output: "thinking no close answer",
		},
		{
			name:   "malformed — closing tag only",
			input:  "before</thinking>after",
			output: "beforeafter",
		},
		{
			name:   "unclosed final",
			input:  "<final>never closes",
			output: "never closes",
		},
		{
			name:   "tool-call spill-over",
			input:  "hello <tool_call>{}</tool_call> more",
			output: "hello  more",
		},
		{
			name:   "multiple tool-call spills",
			input:  "a<tool_call>x</tool_call>b<tool_call>y</tool_call>c",
			output: "abc",
		},
		{
			name:   "function block strip",
			input:  "before <function name=\"search\">query</function> after",
			output: "before  after",
		},
		{
			name:   "parameter block strip",
			input:  "before <parameter name=\"q\">hello</parameter> after",
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
			name:   "collapses 3+ newlines to 2",
			input:  "a\n\n\n\nb",
			output: "a\n\nb",
		},
		{
			name:   "preserves single newlines",
			input:  "line 1\nline 2",
			output: "line 1\nline 2",
		},
		{
			name:   "final wrapper with multiline inner content",
			input:  "<final>\nline 1\nline 2\n</final>",
			output: "line 1\nline 2",
		},
		{
			name:   "empty final wrapper",
			input:  "<final></final>",
			output: "",
		},
		{
			name:   "self-closing thinking tag",
			input:  "before<thinking/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing thinking tag with space",
			input:  "before<thinking />after",
			output: "beforeafter",
		},
		{
			name:   "self-closing thinking tag with extra whitespace",
			input:  "before<thinking  />after",
			output: "beforeafter",
		},
		{
			name:   "self-closing thinking tag with attributes",
			input:  `before<thinking reason="r"/>after`,
			output: "beforeafter",
		},
		{
			name:   "self-closing thinking tag uppercase",
			input:  "before<THINKING/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing function tag",
			input:  "before<function/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing function tag with attributes",
			input:  `before<function name="search"/>after`,
			output: "beforeafter",
		},
		{
			name:   "self-closing parameter tag",
			input:  "before<parameter/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing parameter tag with attributes",
			input:  `before<parameter name="q"/>after`,
			output: "beforeafter",
		},
		{
			name:   "self-closing final tag",
			input:  "before<final/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing final tag with space",
			input:  "before<final />after",
			output: "beforeafter",
		},
		{
			name:   "self-closing final tag mixed case",
			input:  "before<FINAL/>after",
			output: "beforeafter",
		},
		{
			name:   "self-closing tag does not consume surrounding non-tag",
			input:  "xaaathinkingx/by",
			output: "xaaathinkingx/by",
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
