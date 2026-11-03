package agent

import (
	"bytes"
	"context"
	"errors"
	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/webfetch"
	"log"
	"strings"
	"testing"
)

func TestOutcomeWebFetchLogSafety(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"failure", true}, {"success", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const marker = "synthetic-fetch-secret"
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			a := &Agent{user: &config.UserConfig{Name: "test", Role: "parent"}, cfg: &config.Config{Tools: config.ToolsConfig{WebFetch: config.WebFetchConfig{Enabled: true, URLAllowlist: []string{"example.test"}, MaxBytes: 1024, TimeoutSec: 1}}}, webFetcher: func(context.Context, string, webfetch.Options) (*webfetch.Result, error) {
				if tc.fail {
					return nil, errors.New(marker)
				}
				return &webfetch.Result{StatusCode: 200, Text: "safe-success"}, nil
			}}
			rawURL := "https://example.test/ordinary"
			if tc.fail {
				rawURL = "https://example.test/" + marker
			}
			out, err := a.handleWebFetch(context.Background(), map[string]any{"url": rawURL})
			if strings.Contains(logs.String(), marker) {
				t.Error("sensitive error or URL leaked to logs")
			}
			if tc.fail && err == nil {
				t.Error("error not preserved")
			}
			if !tc.fail && (err != nil || !strings.Contains(out, "safe-success")) {
				t.Error("successful result not preserved")
			}
		})
	}
}
