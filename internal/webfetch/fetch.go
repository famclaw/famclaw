package webfetch

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

const defaultMaxBytes = 256 * 1024
const defaultTimeout = 15 * time.Second
const defaultUserAgent = "famclaw-webfetch/1 (+https://github.com/famclaw/famclaw)"

var defaultAllowedTypes = []string{"text/html", "text/plain", "application/json", "application/xhtml+xml"}

type Options struct {
	MaxBytes     int64
	Timeout      time.Duration
	AllowedTypes []string
	UserAgent    string
}

type Result struct {
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
	Text        string `json:"text"`
}

func Fetch(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	if len(opts.AllowedTypes) == 0 {
		opts.AllowedTypes = defaultAllowedTypes
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("web fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)

	client := &http.Client{
		Timeout: opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web fetch: do: %w", err)
	}
	defer resp.Body.Close()

	res := &Result{
		URL:         rawURL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
	}

	mediaType, _, mtErr := mime.ParseMediaType(res.ContentType)
	if mtErr != nil {
		mediaType = res.ContentType
	}

	allowed := false
	for _, t := range opts.AllowedTypes {
		if t == mediaType {
			allowed = true
			break
		}
	}
	if !allowed {
		return res, fmt.Errorf("web fetch: content type %q not allowed", mediaType)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, fmt.Errorf("web fetch: non-2xx status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return res, fmt.Errorf("web fetch: read body: %w", err)
	}

	if int64(len(body)) > opts.MaxBytes {
		res.Truncated = true
		body = body[:opts.MaxBytes]
	}
	res.Bytes = int64(len(body))

	switch mediaType {
	case "text/html", "application/xhtml+xml":
		text, err := ExtractText(body)
		if err != nil {
			return res, fmt.Errorf("web fetch: extract: %w", err)
		}
		res.Text = text
	default:
		res.Text = string(body)
	}

	return res, nil
}
