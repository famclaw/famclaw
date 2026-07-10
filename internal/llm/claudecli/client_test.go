//go:build claude_cli_integration

package claudecli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famclaw/famclaw/internal/llm"
	"github.com/famclaw/famclaw/internal/llm/claudecli"
)

// stubClaudeBin writes a stub `claude` shell script to dir and returns the script path.
func stubClaudeBin(t *testing.T, dir, script string) {
	t.Helper()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte(script), 0755); err != nil {
		t.Fatalf("writing stub claude: %v", err)
	}
}

// prependPath prepends dir to $PATH and registers a cleanup to restore the original.
func prependPath(t *testing.T, dir string) {
	t.Helper()
	original := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+original); err != nil {
		t.Fatalf("setting PATH: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Setenv("PATH", original); err != nil {
			t.Errorf("restoring PATH: %v", err)
		}
	})
}

// TestChatPassesSystemPromptToCLIClient verifies the fix for:
// claude_cli backend drops the system prompt.
// The fake claude binary records all arguments to a file; the test asserts
// that the system-prompt marker string reaches the CLI invocation.
func TestChatPassesSystemPromptToCLIClient(t *testing.T) {
	marker := "FAMCLAW_SYSTEM_PROMPT_MARKER_7a3b"

	dir := t.TempDir()

	// Use $0 (script path) to derive output location — works regardless of CWD.
	stubScript := `#!/bin/sh
SCRIPT_DIR=$(dirname "$0")
printf '%s\n' "$@" > "$SCRIPT_DIR/args.txt" 2>&1
cat > "$SCRIPT_DIR/stdin.txt"
echo '{"type":"message_stop","stop_reason":"end_turn"}'
`
	stubClaudeBin(t, dir, stubScript)
	prependPath(t, dir)

	client := claudecli.New()

	messages := []llm.Message{
		{Role: "system", Content: marker},
		{Role: "user", Content: "hello"},
	}

	ctx := context.Background()
	_, err := client.ChatSync(ctx, messages, 0, 0)
	if err != nil {
		t.Fatalf("ChatSync failed: %v", err)
	}

	argsPath := filepath.Join(dir, "args.txt")
	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("reading args.txt: %v", err)
	}

	if !strings.Contains(string(contents), marker) {
		t.Errorf("system prompt marker %q not found in claude invocation args\nFull args:\n%s", marker, string(contents))
	}

	// Also verify the system message is NOT duplicated in stdin.
	stdinPath := filepath.Join(dir, "stdin.txt")
	stdinContents, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("reading stdin.txt: %v", err)
	}
	stdinStr := string(stdinContents)
	if strings.Contains(stdinStr, marker) {
		t.Errorf("system prompt marker %q found in stdin — should be excluded (sent via --system-prompt)\nStdin:\n%s", marker, stdinStr)
	}
}

// TestChatExcludesSystemFromStdin verifies that system messages are sent
// via --system-prompt and NOT included in the NDJSON stdin payload.
func TestChatExcludesSystemFromStdin(t *testing.T) {
	dir := t.TempDir()

	stubScript := `#!/bin/sh
SCRIPT_DIR=$(dirname "$0")
printf '%s\n' "$@" > "$SCRIPT_DIR/args.txt" 2>&1
cat > "$SCRIPT_DIR/stdin.txt"
echo '{"type":"message_stop","stop_reason":"end_turn"}'
`
	stubClaudeBin(t, dir, stubScript)
	prependPath(t, dir)

	client := claudecli.New()

	messages := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Hi, how can I help?"},
	}

	ctx := context.Background()
	_, err := client.ChatSync(ctx, messages, 0, 0)
	if err != nil {
		t.Fatalf("ChatSync failed: %v", err)
	}

	// Verify system prompt flag is present.
	argsPath := filepath.Join(dir, "args.txt")
	argsContents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("reading args.txt: %v", err)
	}
	if !strings.Contains(string(argsContents), "--system-prompt") {
		t.Errorf("--system-prompt flag not found in args\nFull args:\n%s", string(argsContents))
	}

	// Verify system message content is NOT in stdin.
	stdinPath := filepath.Join(dir, "stdin.txt")
	stdinContents, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("reading stdin.txt: %v", err)
	}
	stdinStr := string(stdinContents)
	if strings.Contains(stdinStr, "You are a helpful assistant.") {
		t.Errorf("system prompt found in stdin — should be excluded\nStdin:\n%s", stdinStr)
	}
	if strings.Contains(stdinStr, `"role":"system"`) {
		t.Errorf(`"role":"system" found in stdin — should be excluded\nStdin:\n%s`, stdinStr)
	}

	// Verify only non-system messages are in stdin.
	lines := strings.Split(strings.TrimSpace(stdinStr), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 NDJSON lines (non-system only), got %d", len(lines))
	}
	assertContains(t, lines[0], `"role":"user"`, "first non-system line")
	assertContains(t, lines[1], `"role":"assistant"`, "second non-system line")
}

// TestChatPassesHistoryToCLIClient verifies that conversation history
// (assistant + user turns) reaches the claude CLI via stdin as NDJSON.
func TestChatPassesHistoryToCLIClient(t *testing.T) {
	dir := t.TempDir()

	// Stub captures args and stdin, returns a minimal stream-json response.
	stubScript := `#!/bin/sh
SCRIPT_DIR=$(dirname "$0")
printf '%s\n' "$@" > "$SCRIPT_DIR/args.txt" 2>&1
cat > "$SCRIPT_DIR/stdin.txt"
echo '{"type":"message_stop","stop_reason":"end_turn"}'
`
	stubClaudeBin(t, dir, stubScript)
	prependPath(t, dir)

	client := claudecli.New()

	messages := []llm.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello back"},
		{Role: "user", Content: "how are you"},
	}

	ctx := context.Background()
	_, err := client.ChatSync(ctx, messages, 0, 0)
	if err != nil {
		t.Fatalf("ChatSync failed: %v", err)
	}

	// Verify CLI was invoked with correct flags.
	argsPath := filepath.Join(dir, "args.txt")
	argsContents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("reading args.txt: %v", err)
	}
	argsStr := string(argsContents)
	assertContains(t, argsStr, "-p", "-p flag")
	assertContains(t, argsStr, "--input-format", "--input-format flag")
	assertContains(t, argsStr, "stream-json", "stream-json input format")

	// Verify messages were sent to stdin as NDJSON (one JSON object per line).
	stdinPath := filepath.Join(dir, "stdin.txt")
	stdinContents, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("reading stdin.txt: %v", err)
	}
	stdinStr := string(stdinContents)
	lines := strings.Split(strings.TrimSpace(stdinStr), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 NDJSON lines, got %d", len(lines))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
			t.Errorf("line %d: expected JSON object (starts with {), got: %s", i, line)
		}
	}
	assertContains(t, lines[0], `"role":"user"`, "first line: user role")
	assertContains(t, lines[0], "hi", "first line: user content")
	assertContains(t, lines[1], `"role":"assistant"`, "second line: assistant role")
	assertContains(t, lines[1], "hello back", "second line: assistant content")
	assertContains(t, lines[2], `"role":"user"`, "third line: user role")
	assertContains(t, lines[2], "how are you", "third line: user content")
}

func assertContains(t *testing.T, s, sub, label string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: expected %q in args\nFull args:\n%s", label, sub, s)
	}
}

func TestClient(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		messages    []llm.Message
		wantContain string
		wantErr     bool
		cancelCtx   bool
	}{
		{
			name: "normal response",
			script: `#!/bin/sh
cat > /dev/null
echo '{"type":"message","message":{"content":[{"type":"text","text":"hello from stub"}],"stop_reason":"end_turn"}}'
`,
			messages: []llm.Message{
				{Role: "user", Content: "hello"},
			},
			wantContain: "hello from stub",
		},
		{
			name: "empty messages slice returns no error and empty string",
			script: `#!/bin/sh
echo "should not reach here"
`,
			messages:    []llm.Message{},
			wantContain: "",
		},
		{
			name: "context cancellation kills subprocess",
			// Sleep long enough that cancellation will fire first.
			script: `#!/bin/sh
sleep 5
echo "should not reach here"
`,
			messages: []llm.Message{
				{Role: "user", Content: "ping"},
			},
			wantErr:   true,
			cancelCtx: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stubClaudeBin(t, dir, tc.script)
			prependPath(t, dir)

			client := claudecli.New()

			ctx := context.Background()
			var cancel context.CancelFunc
			if tc.cancelCtx {
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}

			got, err := client.ChatSync(ctx, tc.messages, 0, 0)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error but got nil (response: %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantContain != "" && !containsSubstring(got, tc.wantContain) {
				t.Errorf("response %q does not contain %q", got, tc.wantContain)
			}
		})
	}
}

func TestChatWithToolsNotSupported(t *testing.T) {
	client := claudecli.New()
	msg, err := client.ChatWithTools(context.Background(), nil, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error from ChatWithTools, got nil")
	}
	if msg != nil {
		t.Errorf("expected nil message, got %+v", msg)
	}
	want := "not supported in v1"
	if !containsSubstring(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
