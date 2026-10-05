package web

import (
	"strings"
	"testing"
)

// TestIndexHasNoDeadLocalOllamaPath guards against the reintroduction of the
// legacy local-Ollama setup path in the web UI. The "Run AI locally" card and
// its installOllamaLocal()/chooseLocal() handlers POSTed to /api/setup/ollama,
// which has no Go server route, so the flow always 404'd. These strings must
// stay out of the embedded index.html.
func TestIndexHasNoDeadLocalOllamaPath(t *testing.T) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	src := string(data)

	cases := []struct{ name, needle string }{
		{"no /api/setup/ollama endpoint", "/api/setup/ollama"},
		{"no installOllamaLocal function", "installOllamaLocal"},
		{"no chooseLocal function", "chooseLocal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(src, tc.needle) {
				t.Errorf("index.html still contains %q", tc.needle)
			}
		})
	}
}
