package agent

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/famclaw/famclaw/internal/config"
)

func TestHandleWebSearchSuccessDoesNotLogPrivateDetails(t *testing.T) {
	// Isolate the process-wide logger from other tests and background goroutines.
	if os.Getenv("FAMCLAW_SEARCH_LOG_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHandleWebSearchSuccessDoesNotLogPrivateDetails$")
		cmd.Env = append(os.Environ(), "FAMCLAW_SEARCH_LOG_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated test: %v\n%s", err, output)
		}
		return
	}

	const user = "fake-search-user"
	const query = "fake-private-query"
	const token = "fake-url-token"
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("q") != query {
			t.Errorf("provider received query %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"title":"Found","url":"https://example.com/?token=%s","content":"result snippet"}]}`, token)
	}))
	defer server.Close()

	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	a := &Agent{user: &config.UserConfig{Name: user}, cfg: staleSearchTestCfg(server.URL)}
	result, err := a.handleWebSearch(context.Background(), map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(result, "Found") || !strings.Contains(result, token) {
		t.Errorf("requests = %d, result = %q; expected one result with its URL", requests, result)
	}
	for _, private := range []string{user, query, token} {
		if strings.Contains(logs.String(), private) {
			t.Errorf("search log contains private fixture %q: %q", private, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "hits=1") {
		t.Errorf("search log missing hit count: %q", logs.String())
	}
}
