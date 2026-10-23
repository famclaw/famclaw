package mcpserver

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// testSerial is the online device serial used across dispatch tests; it
// must never leak into a tool result (design §5.9 no-leak).
const testSerial = "emulator-5554"

// testPNG is a minimal PNG-magic payload for screenshot fixtures.
var testPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}

// findDump is a small uiautomator hierarchy exercising text,
// content-desc, resource-id and a plain background node.
const findDump = `<hierarchy>
  <node text="Hello World" resource-id="com.test:id/greet" class="android.widget.TextView"
        package="com.test" bounds="[10,20][310,120]" enabled="true" clickable="false"/>
  <node text="" resource-id="" class="android.widget.FrameLayout" package="com.test"
        content-desc="Close button" bounds="[300,0][360,60]" enabled="true" clickable="true"/>
  <node text="" resource-id="" class="android.view.View" package="com.test"
        bounds="[0,0][100,100]" enabled="true" clickable="false"/>
</hierarchy>`

// readyFixtures registers the adb responses for a fully ready, unlocked
// device on testSerial.
func readyFixtures(f *fakeADB) {
	f.setDevices(testSerial)
	f.Set("-s emulator-5554 get-state", "device")
	f.Set("-s emulator-5554 shell getprop sys.boot_completed", "1")
	f.Set("-s emulator-5554 shell dumpsys power", "Display Power: state=ON")
	f.Set("-s emulator-5554 shell dumpsys window", "mDreamingLockscreen=false")
	f.Set("-s emulator-5554 shell dumpsys activity activities",
		"mResumedActivity=ActivityRecord{abc u0 com.example.app/.MainActivity t42}")
}

// callTool invokes a tool on the in-process client and fails the test on a
// transport or tool-level error.
func callTool(t *testing.T, c *client.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s): error result: %s", name, resultText(res))
	}
	return res
}

func TestDispatch_DeviceStatus(t *testing.T) {
	readyClient := func(t *testing.T) *client.Client {
		fake := newFakeADB()
		readyFixtures(fake)
		return inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))
	}
	t.Run("ready", func(t *testing.T) {
		res := callTool(t, readyClient(t), "device.status", nil)
		got := resultText(res)
		for _, want := range []string{`"state":"ready"`, `"booted":true`, `"screen_on":true`, `"foreground":"com.example.app"`} {
			if !strings.Contains(got, want) {
				t.Errorf("result missing %s:\n%s", want, got)
			}
		}
	})
	t.Run("no device is down, not an error", func(t *testing.T) {
		fake := newFakeADB()
		fake.setDevices() // zero online devices
		c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))
		res := callTool(t, c, "device.status", nil)
		if got := resultText(res); !strings.Contains(got, `"state":"down"`) {
			t.Errorf("want state down, got: %s", got)
		}
	})
}

func TestDispatch_Screenshot(t *testing.T) {
	fake := newFakeADB()
	fake.setDevices(testSerial)
	fake.Set("-s emulator-5554 exec-out screencap -p", string(testPNG))
	c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

	res := callTool(t, c, "screenshot", nil)
	var img *mcp.ImageContent
	for i := range res.Content {
		if ic, ok := res.Content[i].(mcp.ImageContent); ok {
			img = &ic
		}
	}
	if img == nil {
		t.Fatalf("no ImageContent in result: %+v", res.Content)
	}
	if img.MIMEType != "image/png" {
		t.Errorf("want mime image/png, got %q", img.MIMEType)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	if !reflect.DeepEqual(decoded, testPNG) {
		t.Errorf("decoded PNG mismatch:\n got %q\nwant %q", decoded, testPNG)
	}
}

func TestDispatch_Find(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		wants  []string // substrings that must appear
		noHits bool     // expect the no-match message
	}{
		{name: "text match", args: map[string]any{"text": "hello"}, wants: []string{`"text":"Hello World"`, `"x":160`, `"y":70`}},
		{name: "desc match", args: map[string]any{"content_desc": "close"}, wants: []string{`"desc":"Close button"`, `"clickable":true`}},
		{name: "id match", args: map[string]any{"resource_id": "greet"}, wants: []string{`"id":"com.test:id/greet"`}},
		{name: "no match", args: map[string]any{"text": "nonexistent"}, noHits: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeADB()
			fake.setDevices(testSerial)
			fake.Set("-s emulator-5554 shell uiautomator dump /sdcard/ui.xml", "")
			fake.Set("-s emulator-5554 exec-out cat /sdcard/ui.xml", findDump)
			c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

			res := callTool(t, c, "find", tc.args)
			got := resultText(res)
			if tc.noHits {
				if !strings.Contains(got, "No elements matched") {
					t.Errorf("want no-match message, got: %s", got)
				}
				return
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("result missing %s:\n%s", want, got)
				}
			}
		})
	}
}

// TestNoLeak_Serial asserts the device serial never appears in any read
// tool's result text or error (design §5.9).
func TestNoLeak_Serial(t *testing.T) {
	fake := newFakeADB()
	readyFixtures(fake)
	fake.Set("-s emulator-5554 exec-out screencap -p", string(testPNG))
	fake.Set("-s emulator-5554 shell uiautomator dump /sdcard/ui.xml", "")
	fake.Set("-s emulator-5554 exec-out cat /sdcard/ui.xml", findDump)
	c := inProcessClientWithExecutor(t, initPolicy(false), newTestExecutor(t, fake))

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "device.status"},
		{name: "screenshot"},
		{name: "find", args: map[string]any{"text": "hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, c, tc.name, tc.args)
			for _, content := range res.Content {
				if tc, ok := content.(mcp.TextContent); ok && strings.Contains(tc.Text, testSerial) {
					t.Errorf("serial %q leaked in result: %s", testSerial, tc.Text)
				}
			}
		})
	}
}
