package browser

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
)

// connectTestPage connects to a Playwright server using FAMCLAW_PLAYWRIGHT_WS
// and returns a fresh page plus a cleanup function. If the env var is unset or
// the connection fails the test is skipped — the orchestrator does not expose a
// Playwright server, so tests skip cleanly in CI.
func connectTestPage(t *testing.T) (playwright.Page, func()) {
	t.Helper()
	wsURL := os.Getenv("FAMCLAW_PLAYWRIGHT_WS")
	if wsURL == "" {
		t.Skip("FAMCLAW_PLAYWRIGHT_WS not set — no Playwright server available")
	}
	pw, err := playwright.Run()
	if err != nil {
		t.Skipf("playwright.Run: %v", err)
	}
	br, err := pw.Chromium.Connect(wsURL)
	if err != nil {
		_ = pw.Stop()
		t.Skipf("playwright connect %s: %v", wsURL, err)
	}
	bctx, err := br.NewContext()
	if err != nil {
		_ = br.Close()
		_ = pw.Stop()
		t.Skipf("NewContext: %v", err)
	}
	page, err := bctx.NewPage()
	if err != nil {
		_ = bctx.Close()
		_ = br.Close()
		_ = pw.Stop()
		t.Skipf("NewPage: %v", err)
	}
	cleanup := func() {
		_ = bctx.Close()
		_ = br.Close()
		_ = pw.Stop()
	}
	return page, cleanup
}

// hasUnprefixedRef returns true if any line in formatted starts with an
// unprefixed ref id (e<digit>) rather than a new-element marker (*e<digit>).
func hasUnprefixedRef(formatted string) bool {
	for _, line := range strings.Split(formatted, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) > 1 && trimmed[0] == 'e' && trimmed[1] >= '0' && trimmed[1] <= '9' {
			return true
		}
	}
	return false
}

// hasStarRef returns true if any line in formatted contains a "*e<N>" marker.
func hasStarRef(formatted string) bool {
	for _, line := range strings.Split(formatted, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "*e") {
			return true
		}
	}
	return false
}

// TestBuildSnapshot_FirstCallAllNew verifies that on the very first call
// (empty prevRefs) every interactive ref in the output is marked as new
// with a "*" prefix — because nothing was seen before.
func TestBuildSnapshot_FirstCallAllNew(t *testing.T) {
	page, cleanup := connectTestPage(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>One</button><button>Two</button></body></html>`)
	}))
	defer srv.Close()

	if _, err := page.Goto(srv.URL); err != nil {
		t.Fatalf("Goto: %v", err)
	}

	formatted, _, newPrevRefs, err := buildSnapshot(page, nil)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	if len(newPrevRefs) == 0 {
		t.Fatal("expected at least one interactive ref, got none")
	}
	if hasUnprefixedRef(formatted) {
		t.Errorf("expected all refs to be new (*e...) on first call, but got unprefixed refs in:\n%s", formatted)
	}
	if !hasStarRef(formatted) {
		t.Errorf("expected at least one *e... ref in first-call output:\n%s", formatted)
	}
}

// TestBuildSnapshot_SecondCallReusesRefs verifies that snapshotting the same
// page twice returns identical ref ids with no "*" prefix on the second call.
func TestBuildSnapshot_SecondCallReusesRefs(t *testing.T) {
	page, cleanup := connectTestPage(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>Alpha</button><button>Beta</button></body></html>`)
	}))
	defer srv.Close()

	if _, err := page.Goto(srv.URL); err != nil {
		t.Fatalf("Goto: %v", err)
	}

	_, refs1, prevRefs1, err := buildSnapshot(page, nil)
	if err != nil {
		t.Fatalf("first buildSnapshot: %v", err)
	}

	formatted2, refs2, _, err := buildSnapshot(page, prevRefs1)
	if err != nil {
		t.Fatalf("second buildSnapshot: %v", err)
	}

	// No "*" prefixed refs on second call — all elements are unchanged.
	if hasStarRef(formatted2) {
		t.Errorf("expected no new-element markers on second call, got:\n%s", formatted2)
	}

	// Every ref from the first snapshot must appear with the same entry.
	for ref, e1 := range refs1 {
		e2, ok := refs2[ref]
		if !ok {
			t.Errorf("ref %q from first snapshot missing in second", ref)
			continue
		}
		if e1 != e2 {
			t.Errorf("ref %q changed between snapshots: %+v → %+v", ref, e1, e2)
		}
	}
}

// TestBuildSnapshot_NewElementGetsStarPrefix verifies that when a second button
// appears after the first snapshot, the existing button keeps its ref id and
// the new button is marked with a "*" prefix.
func TestBuildSnapshot_NewElementGetsStarPrefix(t *testing.T) {
	page, cleanup := connectTestPage(t)
	defer cleanup()

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>Existing</button></body></html>`)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>Existing</button><button>NewButton</button></body></html>`)
	}))
	defer srv2.Close()

	if _, err := page.Goto(srv1.URL); err != nil {
		t.Fatalf("Goto srv1: %v", err)
	}
	_, refs1, prevRefs1, err := buildSnapshot(page, nil)
	if err != nil {
		t.Fatalf("first buildSnapshot: %v", err)
	}

	if _, err := page.Goto(srv2.URL); err != nil {
		t.Fatalf("Goto srv2: %v", err)
	}
	formatted2, refs2, _, err := buildSnapshot(page, prevRefs1)
	if err != nil {
		t.Fatalf("second buildSnapshot: %v", err)
	}

	// The original "Existing" button must keep its ref id.
	var existingRef string
	for ref, e := range refs1 {
		if e.Name == "Existing" {
			existingRef = ref
			break
		}
	}
	if existingRef == "" {
		t.Fatal("could not find 'Existing' button ref in first snapshot")
	}
	if _, ok := refs2[existingRef]; !ok {
		t.Errorf("existing button ref %q not found in second snapshot refs", existingRef)
	}

	// The new "NewButton" must appear with a "*" prefix.
	hasNewStar := false
	for _, line := range strings.Split(formatted2, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "*e") && strings.Contains(line, "NewButton") {
			hasNewStar = true
		}
	}
	if !hasNewStar {
		t.Errorf("expected 'NewButton' to have *-prefixed ref in:\n%s", formatted2)
	}
}

// TestBuildSnapshot_DisappearedElementGCd verifies that elements absent from
// the current page are not carried forward in newPrevRefs.
func TestBuildSnapshot_DisappearedElementGCd(t *testing.T) {
	page, cleanup := connectTestPage(t)
	defer cleanup()

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>A</button><button>B</button><button>C</button></body></html>`)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>A</button><button>B</button></body></html>`)
	}))
	defer srv2.Close()

	if _, err := page.Goto(srv1.URL); err != nil {
		t.Fatalf("Goto srv1: %v", err)
	}
	_, _, prevRefs1, err := buildSnapshot(page, nil)
	if err != nil {
		t.Fatalf("first buildSnapshot: %v", err)
	}
	if len(prevRefs1) != 3 {
		t.Fatalf("expected 3 prevRefs after first call, got %d", len(prevRefs1))
	}

	if _, err := page.Goto(srv2.URL); err != nil {
		t.Fatalf("Goto srv2: %v", err)
	}
	_, _, newPrevRefs, err := buildSnapshot(page, prevRefs1)
	if err != nil {
		t.Fatalf("second buildSnapshot: %v", err)
	}
	if len(newPrevRefs) != 2 {
		t.Errorf("expected 2 entries in newPrevRefs after C disappears, got %d: %v", len(newPrevRefs), newPrevRefs)
	}
}

// TestBuildSnapshot_RefIdsContiguousAfterDisappearance verifies that when a
// button disappears and a new one appears, the new element receives a
// well-formed e<N> ref id (parseable integer suffix). The exact number is
// unspecified — this test only checks the format invariant.
func TestBuildSnapshot_RefIdsContiguousAfterDisappearance(t *testing.T) {
	page, cleanup := connectTestPage(t)
	defer cleanup()

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>Keep</button><button>Gone</button></body></html>`)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button>Keep</button><button>Fresh</button></body></html>`)
	}))
	defer srv2.Close()

	if _, err := page.Goto(srv1.URL); err != nil {
		t.Fatalf("Goto srv1: %v", err)
	}
	_, _, prevRefs1, err := buildSnapshot(page, nil)
	if err != nil {
		t.Fatalf("first buildSnapshot: %v", err)
	}

	if _, err := page.Goto(srv2.URL); err != nil {
		t.Fatalf("Goto srv2: %v", err)
	}
	_, refs2, _, err := buildSnapshot(page, prevRefs1)
	if err != nil {
		t.Fatalf("second buildSnapshot: %v", err)
	}

	// "Fresh" must appear in refs2 with a parseable e<N> id.
	var freshRef string
	for ref, e := range refs2 {
		if e.Name == "Fresh" {
			freshRef = ref
			break
		}
	}
	if freshRef == "" {
		t.Fatal("could not find 'Fresh' button in second snapshot")
	}
	var n int
	if _, scanErr := fmt.Sscanf(freshRef, "e%d", &n); scanErr != nil {
		t.Errorf("new element ref %q is not a parseable e<N> integer: %v", freshRef, scanErr)
	}
}
