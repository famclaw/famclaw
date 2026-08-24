// Dispatch glue tests for builtin__send_file: the agent must route the
// tool call to the current conversation's gateway FileSender with the
// destination taken from the message context (group channel vs DM) and the
// sandbox root taken from the agent's effective conversation sandbox.
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/gateway"
)

type capturingFileSender struct {
	destination gateway.OutboundDestination
	file        gateway.OutboundFile
	caption     string
	err         error
}

func (c *capturingFileSender) SendFile(ctx context.Context, dest gateway.OutboundDestination, file gateway.OutboundFile, caption string) error {
	c.destination = dest
	c.file = file
	c.caption = caption
	return c.err
}

func newSendFileAgent(t *testing.T, sender gateway.FileSender, msgCtx gateway.MsgContext) (*Agent, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("hello report"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	a := &Agent{
		user:                 &config.UserConfig{Name: "dep", DisplayName: "Dep", Role: "parent"},
		msgContext:           msgCtx,
		effectiveSandboxRoot: root,
		fileSenderRegistry:   map[string]gateway.FileSender{"discord": sender},
	}
	return a, root
}

func TestBuiltinSendFileDispatch(t *testing.T) {
	tests := []struct {
		name        string
		msgCtx      gateway.MsgContext
		args        map[string]any
		senderErr   error
		wantErr     string
		wantGroup   string
		wantCaption string
	}{
		{
			name:        "group conversation delivers to the channel",
			msgCtx:      gateway.MsgContext{Gateway: "discord", ExternalID: "user-9", GroupID: "channel-77"},
			args:        map[string]any{"path": "report.md", "caption": "here is the report"},
			wantGroup:   "channel-77",
			wantCaption: "here is the report",
		},
		{
			name:   "dm conversation delivers without a group id",
			msgCtx: gateway.MsgContext{Gateway: "discord", ExternalID: "user-9"},
			args:   map[string]any{"path": "report.md"},
		},
		{
			// The target does not exist, so containment fails at the
			// symlink-resolution step — either way the rejection happens
			// before any delivery (the filesend package pins the
			// "escapes sandbox" message with a real outside file).
			name:      "traversal path is rejected before any delivery",
			msgCtx:    gateway.MsgContext{Gateway: "discord", ExternalID: "user-9", GroupID: "channel-77"},
			args:      map[string]any{"path": "../secret.txt"},
			wantErr:   "resolving send_file path",
			wantGroup: "",
		},
		{
			name:      "gateway without a FileSender fails honestly",
			msgCtx:    gateway.MsgContext{Gateway: "telegram", ExternalID: "user-9"},
			args:      map[string]any{"path": "report.md"},
			wantErr:   "file delivery is not available",
			wantGroup: "",
		},
		{
			name:      "sender error surfaces in the tool result",
			msgCtx:    gateway.MsgContext{Gateway: "discord", ExternalID: "user-9", GroupID: "channel-77"},
			args:      map[string]any{"path": "report.md"},
			senderErr: errForTest("discord rate limited"),
			wantErr:   "discord rate limited",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &capturingFileSender{err: tc.senderErr}
			a, _ := newSendFileAgent(t, sender, tc.msgCtx)
			handler := a.makeBuiltinHandler()
			out, err := handler(context.Background(), "builtin__send_file", tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("handler = %q, want error containing %q", out, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !strings.Contains(out, "report.md") {
				t.Errorf("confirmation = %q, want to mention report.md", out)
			}
			if sender.destination.ExternalID != "user-9" {
				t.Errorf("destination.ExternalID = %q, want user-9", sender.destination.ExternalID)
			}
			if sender.destination.GroupID != tc.wantGroup {
				t.Errorf("destination.GroupID = %q, want %q", sender.destination.GroupID, tc.wantGroup)
			}
			if sender.caption != tc.wantCaption {
				t.Errorf("caption = %q, want %q", sender.caption, tc.wantCaption)
			}
			if string(sender.file.Data) != "hello report" {
				t.Errorf("file data = %q, want fixture bytes", sender.file.Data)
			}
		})
	}
}

// errForTest returns an error with the given message (avoids importing
// errors just for this helper).
func errForTest(msg string) error { return &testError{msg: msg} }

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
