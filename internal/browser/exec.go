package browser

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// HostValidator returns nil iff host is allowed.
type HostValidator func(host string) error

// ExecInput is the dispatcher argument shape.
type ExecInput struct {
	User      string
	ToolName  string
	Args      map[string]any
	HostCheck HostValidator
}

// Exec dispatches a builtin__browser_* call. Every action that mutates page
// state (navigate, click, fill, select, press_key, wait_for) RETURNS A FRESH
// SNAPSHOT to the caller so the LLM always sees the current interactive
// element list. browser_extract and browser_snapshot are read-only.
func (p *Pool) Exec(ctx context.Context, in ExecInput) (string, error) {
	if strings.TrimSpace(in.User) == "" {
		return "", fmt.Errorf("browser: user is required")
	}
	// browser_done does not require an active page session — handle before
	// getOrCreateSession to avoid spinning up a Playwright tab for a signal
	// that carries no page interaction.
	if in.ToolName == "builtin__browser_done" {
		return p.doDone(in.Args)
	}
	sess, err := p.getOrCreateSession(in.User)
	if err != nil {
		return "", err
	}
	switch in.ToolName {
	case "builtin__browser_navigate":
		return p.doNavigate(ctx, sess, in.Args, in.HostCheck)
	case "builtin__browser_snapshot":
		return p.doSnapshot(sess)
	case "builtin__browser_click":
		return p.doClick(sess, in.Args)
	case "builtin__browser_fill":
		return p.doFill(sess, in.Args)
	case "builtin__browser_select":
		return p.doSelect(sess, in.Args)
	case "builtin__browser_press_key":
		return p.doPressKey(sess, in.Args)
	case "builtin__browser_extract":
		return p.doExtract(sess, in.Args)
	case "builtin__browser_wait_for":
		return p.doWaitFor(sess, in.Args)
	case "builtin__browser_screenshot":
		return p.doScreenshot(sess, in.Args)
	case "builtin__browser_fill_form":
		return p.doFillForm(sess, in.Args)
	default:
		return "", fmt.Errorf("browser: unknown tool %q", in.ToolName)
	}
}

// getOrCreateSession returns the userSession. Exec needs the session
// (not just the Page) to read/write its refs table.
func (p *Pool) getOrCreateSession(user string) (*userSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("browser: pool closed")
	}
	if s, ok := p.sessions[user]; ok {
		s.lastUsed = time.Now()
		return s, nil
	}
	bctx, err := p.browser.NewContext()
	if err != nil {
		return nil, fmt.Errorf("browser: NewContext: %w", err)
	}
	page, err := bctx.NewPage()
	if err != nil {
		_ = bctx.Close()
		return nil, fmt.Errorf("browser: NewPage: %w", err)
	}
	s := &userSession{bctx: bctx, page: page, refs: make(map[string]RefEntry), prevRefs: make(map[string]string), lastUsed: time.Now()}
	p.sessions[user] = s
	return s, nil
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func argBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

// snapshotMaxChars bounds the snapshot text returned to the LLM. Large
// pages (Google Flights results panels can be 40+KB) blow up per-iteration
// LLM latency and cause the bot to "hang" from the user's perspective
// even though tool calls are still happening. We truncate to roughly one
// page worth of refs (~5KB ≈ 1300 tokens). The ref→locator table on the
// session still maps every emitted ref, so truncating just means the LLM
// sees fewer choices per turn — it can refresh with browser_snapshot.
const snapshotMaxChars = 5000

// snapshotReply re-runs the snapshot on the current page and stores the new
// ref table on the session. Returns the formatted-for-LLM text (truncated).
func (p *Pool) snapshotReply(sess *userSession) (string, error) {
	formatted, refs, newPrevRefs, err := buildSnapshot(sess.page, sess.prevRefs)
	if err != nil {
		return "", err
	}
	sess.refs = refs
	sess.prevRefs = newPrevRefs
	title, _ := sess.page.Title()

	limit := p.cfg.SnapshotMaxChars
	if limit <= 0 {
		limit = snapshotMaxChars // default
	}

	body := formatted
	truncated := false
	if len(body) > limit {
		// Cut at last newline before the cap to keep entries intact.
		cut := strings.LastIndex(body[:limit], "\n")
		if cut < 0 {
			cut = limit
		}
		body = body[:cut]
		truncated = true
	}

	header := fmt.Sprintf("URL: %s\nTitle: %s\nInteractive elements (use the ref id with browser_click/browser_fill/browser_select/etc):\n\n",
		sess.page.URL(), title)
	out := header + body
	if truncated {
		out += fmt.Sprintf("\n\n[snapshot truncated at %d chars; %d total refs available. Call browser_snapshot again if you need a fresh view, or just use a ref you've already seen.]", limit, len(refs))
	}
	return out, nil
}

func (p *Pool) doNavigate(_ context.Context, sess *userSession, args map[string]any, hostCheck HostValidator) (string, error) {
	raw := strings.TrimSpace(argString(args, "url"))
	if raw == "" {
		return "", fmt.Errorf("browser_navigate requires 'url'")
	}
	timeout := argInt(args, "timeout_ms", 20000)
	if _, err := p.navigate(sess, raw, hostCheck, timeout); err != nil {
		return "", fmt.Errorf("browser_navigate: %w", err)
	}
	return p.snapshotReply(sess)
}

// navigate is the navigation core shared by builtin__browser_navigate and
// NavigateAndExtract: URL parse, http/https scheme enforcement, the host
// gate before navigation, page.Goto (which follows 3xx redirects), the
// host gate again on the final URL (mirroring the redirect-time host check
// the HTTP web_fetch path enforces), and a reset of the session's ref
// table (navigation invalidates ref continuity). It returns the
// navigation's HTTP response — nil when the browser reported none — so
// callers can inspect response metadata such as the body size.
func (p *Pool) navigate(sess *userSession, raw string, hostCheck HostValidator, timeoutMS int) (playwright.Response, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme %q not allowed", u.Scheme)
	}
	if hostCheck != nil {
		if cerr := hostCheck(u.Hostname()); cerr != nil {
			return nil, cerr
		}
	}
	resp, err := sess.page.Goto(raw, playwright.PageGotoOptions{
		Timeout:   playwright.Float(float64(timeoutMS)),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		return nil, err
	}
	// Re-validate the host after any redirects: page.Goto follows 3xx redirects,
	// so an allowlisted initial URL could land on a disallowed host. Mirrors the
	// redirect-time host check the HTTP web_fetch path enforces.
	if hostCheck != nil {
		if finalURL, perr := url.Parse(sess.page.URL()); perr == nil {
			if cerr := hostCheck(finalURL.Hostname()); cerr != nil {
				return nil, fmt.Errorf("redirected to disallowed host: %w", cerr)
			}
		}
	}
	sess.prevRefs = nil // page navigation invalidates ref continuity
	return resp, nil
}

// NavigateAndExtract is the web_fetch browser-fallback entry point. It
// navigates the user's page to rawURL under the same constraints as
// builtin__browser_navigate (http/https only, the host gate before and
// after redirects) and extracts the rendered body text with the same
// semantics as builtin__browser_extract in text mode (trimmed, 8000-char
// cap). It also returns the size of the navigated document's HTTP response
// body in transferred (wire) bytes: the Content-Length header when
// present, otherwise the length of the received body (the sum of chunk
// payloads for a chunked transfer-encoding response); 0 when the browser
// reported no response or the size could not be determined. The page is
// left at rawURL; the per-user session is released by the idle sweeper,
// the same acquire-use-release lifecycle as Exec.
func (p *Pool) NavigateAndExtract(_ context.Context, user, rawURL string, hostCheck HostValidator) (string, int64, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", 0, fmt.Errorf("browser: user is required")
	}
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", 0, fmt.Errorf("browser: url is required")
	}
	sess, err := p.getOrCreateSession(user)
	if err != nil {
		return "", 0, err
	}
	resp, err := p.navigate(sess, raw, hostCheck, 20000)
	if err != nil {
		return "", 0, fmt.Errorf("navigate %s: %w", raw, err)
	}
	wireBytes, err := responseWireBytes(resp)
	if err != nil {
		return "", 0, fmt.Errorf("navigate %s: %w", raw, err)
	}
	bodyText, err := sess.page.Locator("body").TextContent()
	if err != nil {
		return "", 0, fmt.Errorf("extract %s: %w", raw, err)
	}
	return capExtracted(strings.TrimSpace(bodyText)), wireBytes, nil
}

func (p *Pool) doSnapshot(sess *userSession) (string, error) {
	return p.snapshotReply(sess)
}

func (p *Pool) doClick(sess *userSession, args map[string]any) (string, error) {
	ref := argString(args, "ref")
	if ref == "" {
		return "", fmt.Errorf("browser_click requires 'ref' (use browser_snapshot to get refs)")
	}
	loc, err := resolveRef(sess.page, sess.refs, ref)
	if err != nil {
		return "", err
	}
	timeout := argInt(args, "timeout_ms", 8000)
	if err := loc.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(float64(timeout))}); err != nil {
		return "", fmt.Errorf("browser_click %s: %w", ref, err)
	}
	return p.snapshotReply(sess)
}

func (p *Pool) doFill(sess *userSession, args map[string]any) (string, error) {
	ref := argString(args, "ref")
	val := argString(args, "value")
	if ref == "" {
		return "", fmt.Errorf("browser_fill requires 'ref'")
	}
	loc, err := resolveRef(sess.page, sess.refs, ref)
	if err != nil {
		return "", err
	}
	timeout := argInt(args, "timeout_ms", 8000)
	if err := loc.Fill(val, playwright.LocatorFillOptions{Timeout: playwright.Float(float64(timeout))}); err != nil {
		return "", fmt.Errorf("browser_fill %s: %w", ref, err)
	}
	return p.snapshotReply(sess)
}

func (p *Pool) doSelect(sess *userSession, args map[string]any) (string, error) {
	ref := argString(args, "ref")
	val := argString(args, "value")
	if ref == "" || val == "" {
		return "", fmt.Errorf("browser_select requires 'ref' and 'value'")
	}
	loc, err := resolveRef(sess.page, sess.refs, ref)
	if err != nil {
		return "", err
	}
	timeout := argInt(args, "timeout_ms", 8000)
	vals := []string{val}
	if _, err := loc.SelectOption(playwright.SelectOptionValues{Values: &vals}, playwright.LocatorSelectOptionOptions{Timeout: playwright.Float(float64(timeout))}); err != nil {
		return "", fmt.Errorf("browser_select %s: %w", ref, err)
	}
	return p.snapshotReply(sess)
}

func (p *Pool) doPressKey(sess *userSession, args map[string]any) (string, error) {
	key := argString(args, "key")
	if key == "" {
		return "", fmt.Errorf("browser_press_key requires 'key'")
	}
	if err := sess.page.Keyboard().Press(key); err != nil {
		return "", fmt.Errorf("browser_press_key: %w", err)
	}
	return p.snapshotReply(sess)
}

func (p *Pool) doExtract(sess *userSession, args map[string]any) (string, error) {
	mode := argString(args, "mode")
	if mode == "" {
		mode = "text"
	}
	ref := argString(args, "ref")
	var loc playwright.Locator
	if ref != "" {
		l, err := resolveRef(sess.page, sess.refs, ref)
		if err != nil {
			return "", err
		}
		loc = l
	} else {
		loc = sess.page.Locator("body")
	}
	switch mode {
	case "text":
		txt, err := loc.TextContent()
		if err != nil {
			return "", fmt.Errorf("browser_extract text: %w", err)
		}
		return capExtracted(strings.TrimSpace(txt)), nil
	case "html":
		html, err := loc.InnerHTML()
		if err != nil {
			return "", fmt.Errorf("browser_extract html: %w", err)
		}
		return capExtracted(html), nil
	default:
		return "", fmt.Errorf("browser_extract: unknown mode %q (want text|html)", mode)
	}
}

// capExtracted truncates rendered content to the browser_extract tool's
// 8000-char cap, appending a marker when truncated.
func capExtracted(s string) string {
	if len(s) > 8000 {
		return s[:8000] + "\n[truncated]"
	}
	return s
}

// contentLength parses the response's Content-Length header. The bool is
// false when the header is absent or not a valid non-negative integer —
// both mean "fall back to the received-body size".
func contentLength(headers map[string]string) (int64, bool) {
	raw, ok := headers["content-length"]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// wireBodyBytes returns the size of an HTTP response body in transferred
// (wire) bytes: the Content-Length header when present and parseable as a
// non-negative integer, otherwise the length of the received body — which
// is the sum of the chunk payloads when the response was sent with a
// chunked transfer encoding.
func wireBodyBytes(headers map[string]string, bodyLen int) int64 {
	if n, ok := contentLength(headers); ok {
		return n
	}
	return int64(bodyLen)
}

// responseWireBytes returns the size of resp's body in transferred (wire)
// bytes. A nil resp (the browser reported no response, e.g. a coalesced
// navigation) yields 0. When Content-Length is present the body is not
// fetched; otherwise the received body is read and its length used (the
// sum of chunk payloads for a chunked transfer-encoding response).
func responseWireBytes(resp playwright.Response) (int64, error) {
	if resp == nil {
		return 0, nil
	}
	headers := resp.Headers()
	if n, ok := contentLength(headers); ok {
		return n, nil
	}
	body, err := resp.Body()
	if err != nil {
		return 0, fmt.Errorf("read response body: %w", err)
	}
	return wireBodyBytes(headers, len(body)), nil
}

func (p *Pool) doWaitFor(sess *userSession, args map[string]any) (string, error) {
	ref := argString(args, "ref")
	if ref == "" {
		return "", fmt.Errorf("browser_wait_for requires 'ref'")
	}
	loc, err := resolveRef(sess.page, sess.refs, ref)
	if err != nil {
		return "", err
	}
	timeout := argInt(args, "timeout_ms", 12000)
	err = loc.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(float64(timeout)),
		State:   playwright.WaitForSelectorStateVisible,
	})
	if err != nil {
		return "", fmt.Errorf("browser_wait_for %s: %w", ref, err)
	}
	return p.snapshotReply(sess)
}

func (p *Pool) doScreenshot(sess *userSession, args map[string]any) (string, error) {
	fullPage := argBool(args, "full_page", false)
	buf, err := sess.page.Screenshot(playwright.PageScreenshotOptions{FullPage: playwright.Bool(fullPage)})
	if err != nil {
		return "", fmt.Errorf("browser_screenshot: %w", err)
	}
	return fmt.Sprintf("screenshot %d bytes (PNG; full_page=%v) — binary not rendered to chat", len(buf), fullPage), nil
}

// doFillForm fills multiple form fields in a single call, then returns one
// fresh snapshot at the end. Validation errors stop processing immediately
// and return the offending ref/field index so the LLM can self-correct.
func (p *Pool) doFillForm(sess *userSession, args map[string]any) (string, error) {
	rawFields := args["fields"]
	fields, _ := rawFields.([]any)
	if len(fields) == 0 {
		return "", fmt.Errorf("browser_fill_form: 'fields' must be a non-empty array")
	}
	timeout := argInt(args, "timeout_ms", 8000)
	for i, f := range fields {
		entry, ok := f.(map[string]any)
		if !ok {
			return "", fmt.Errorf("browser_fill_form: field[%d] is not an object", i)
		}
		ref, ok := entry["ref"].(string)
		if !ok || ref == "" {
			return "", fmt.Errorf("browser_fill_form: field[%d] missing 'ref'", i)
		}
		if _, valuePresent := entry["value"]; !valuePresent {
			return "", fmt.Errorf("browser_fill_form: field[%d] missing 'value'", i)
		}
		value, _ := entry["value"].(string)
		loc, err := resolveRef(sess.page, sess.refs, ref)
		if err != nil {
			return "", fmt.Errorf("browser_fill_form ref %s: %w", ref, err)
		}
		if err := loc.Fill(value, playwright.LocatorFillOptions{Timeout: playwright.Float(float64(timeout))}); err != nil {
			return "", fmt.Errorf("browser_fill_form ref %s: %w", ref, err)
		}
	}
	return p.snapshotReply(sess)
}

// doDone signals the end of the tool loop. It returns the summary verbatim
// without touching the page — no snapshot is generated.
func (p *Pool) doDone(args map[string]any) (string, error) {
	summary := argString(args, "summary")
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("browser_done: summary must not be empty")
	}
	return summary, nil
}
