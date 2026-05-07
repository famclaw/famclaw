//go:build claude_cli_integration

package claudecli_test

import (
	"context"
	"os"
	"path/filepath"
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
echo "stub response: $@"
`,
			messages: []llm.Message{
				{Role: "user", Content: "hello"},
			},
			wantContain: "stub response",
		},
		{
			name: "empty messages slice returns no error and empty string",
			script: `#!/bin/sh
echo "stub response: $@"
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
