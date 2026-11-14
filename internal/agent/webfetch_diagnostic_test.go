package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/webfetch"
)

func TestHandleWebFetchFailureDiagnosticsArePrivate(t *testing.T) {
	if os.Getenv("FAMCLAW_FETCH_LOG_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHandleWebFetchFailureDiagnosticsArePrivate$")
		cmd.Env = append(os.Environ(), "FAMCLAW_FETCH_LOG_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated test: %v\n%s", err, output)
		}
		return
	}
	const secret = "fake-private-fetch-detail"
	for _, tc := range []struct {
		name   string
		result *webfetch.Result
		err    error
		want   string
	}{
		{"status", &webfetch.Result{StatusCode: 503}, errors.New(secret), "status=503"},
		{"canceled", nil, fmt.Errorf("%s: %w", secret, context.Canceled), "class=canceled"},
		{"deadline", nil, fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "class=deadline"},
		{"host", nil, webfetch.NewHostNotAllowedError(secret), "class=host_denied"},
		{"dns", nil, &net.DNSError{Err: secret, Name: secret}, "class=dns"},
		{"network", nil, &net.OpError{Op: "dial", Err: errors.New(secret)}, "class=network"},
		{"unknown", nil, errors.New(secret), "class=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			a := &Agent{user: &config.UserConfig{Name: secret}, cfg: &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLAllowlist: []string{"example.test"}, MaxBytes: 1024, TimeoutSec: 1}}}, webFetcher: func(context.Context, string, webfetch.Options) (*webfetch.Result, error) { return tc.result, tc.err }}
			_, err := a.handleWebFetch(context.Background(), map[string]any{"url": "https://example.test/" + secret + "?q=" + secret})
			if err == nil {
				t.Fatal("expected fetch failure")
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Errorf("log %q does not contain %q", logs.String(), tc.want)
			}
			if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "example.test") {
				t.Errorf("private details in log %q", logs.String())
			}
		})
	}
}
