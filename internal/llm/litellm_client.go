package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LiteLLM detection timing. /v1/model/info is a local gateway call that
// should answer in milliseconds; 5s covers slow network shares. 5xx
// responses are retried once after 1s (covers a gateway mid router-reload);
// anything else fails fast.
const (
	detectRequestTimeout = 5 * time.Second
	detectRetryDelay     = 1 * time.Second
)

// LiteLLMClient is a thin read-only client for a LiteLLM (or
// OpenAI-compatible) gateway's model metadata. It discovers each model's
// litellm_params.merge_reasoning_content_in_choices setting so famclaw can
// tell "final answer shipped in the reasoning field" from "chain-of-thought
// shipped in the reasoning field" without operator hand-configuration.
type LiteLLMClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewLiteLLMClient creates a detection client. baseURL is the gateway base
// (e.g. "http://localhost:4001", with or without a trailing /v1). When
// apiKey is non-empty an Authorization: Bearer header is sent — gateways
// that expose model metadata only to authenticated callers need it.
func NewLiteLLMClient(baseURL, apiKey string) *LiteLLMClient {
	return &LiteLLMClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: detectRequestTimeout},
	}
}

// modelInfoEntry is one data[] element of /v1/model/info. ModelName is the
// gateway-level name famclaw chats with; litellm_params carries the
// per-model deployment settings.
type modelInfoEntry struct {
	ModelName     string `json:"model_name"`
	LitellmParams struct {
		MergeReasoning *bool `json:"merge_reasoning_content_in_choices"`
	} `json:"litellm_params"`
}

// modelListEntry is one data[] element of /v1/models. Older LiteLLM builds
// did not expose /v1/model/info and embedded litellm_params in the plain
// model list; newer builds return a list shape without it.
type modelListEntry struct {
	ID            string `json:"id"`
	LitellmParams struct {
		MergeReasoning *bool `json:"merge_reasoning_content_in_choices"`
	} `json:"litellm_params"`
}

// MergeSettings returns the gateway's merge_reasoning_content_in_choices
// setting for every model it knows, keyed by gateway model name. A nil value
// means the gateway reported no setting for that model — callers fall back
// to the built-in heuristic.
//
// Endpoint preference:
//  1. GET /v1/model/info — full litellm_params on current LiteLLM.
//  2. GET /v1/models — only when /v1/model/info does not exist (404).
//     Entries without litellm_params are recorded as nil, so a gateway that
//     supports neither yields an empty-but-valid result (heuristic fallback).
//
// HTTP 5xx responses are retried once after 1s; any other failure
// (network, 4xx, malformed JSON, timeout) is returned as an error.
func (c *LiteLLMClient) MergeSettings(ctx context.Context) (map[string]*bool, error) {
	primary := c.parseModelInfo
	primaryPath := c.modelInfoPath()

	if m, status, err := c.fetch(ctx, primaryPath, primary); err == nil {
		return m, nil
	} else if status != http.StatusNotFound {
		return nil, err
	}

	// /v1/model/info does not exist on this gateway — try the plain list.
	m, _, err := c.fetch(ctx, c.modelListPath(), c.parseModelList)
	return m, err
}

// parseModelInfo decodes a /v1/model/info body.
func (c *LiteLLMClient) parseModelInfo(b []byte) (map[string]*bool, error) {
	var resp struct {
		Data []modelInfoEntry `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parsing /v1/model/info: %w", err)
	}
	out := make(map[string]*bool, len(resp.Data))
	for _, e := range resp.Data {
		if e.ModelName == "" {
			continue
		}
		out[e.ModelName] = e.LitellmParams.MergeReasoning
	}
	return out, nil
}

// parseModelList decodes a /v1/models body.
func (c *LiteLLMClient) parseModelList(b []byte) (map[string]*bool, error) {
	var resp struct {
		Data []modelListEntry `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parsing /v1/models: %w", err)
	}
	out := make(map[string]*bool, len(resp.Data))
	for _, e := range resp.Data {
		// Without litellm_params the gateway says nothing authoritative
		// about this model — record nil so the heuristic decides.
		if e.ID == "" {
			continue
		}
		out[e.ID] = e.LitellmParams.MergeReasoning
	}
	return out, nil
}

// modelInfoPath / modelListPath build endpoint URLs, normalising whether the
// configured base URL already ends in /v1 (mirrors chatEndpoint()).
func (c *LiteLLMClient) modelInfoPath() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/model/info"
	}
	return c.baseURL + "/v1/model/info"
}

func (c *LiteLLMClient) modelListPath() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/models"
	}
	return c.baseURL + "/v1/models"
}

// fetch performs GET path with auth and one 5xx retry. It returns the
// parsed settings, the HTTP status (0 when no response arrived), and an
// error. The status is only meaningful when err != nil, so callers can
// distinguish "endpoint does not exist" (404 → try the fallback endpoint)
// from "endpoint exists but rejected us" (401/403 → fail, do not retry).
func (c *LiteLLMClient) fetch(ctx context.Context, path string, parse func([]byte) (map[string]*bool, error)) (map[string]*bool, int, error) {
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(detectRetryDelay):
			}
		}
		m, status, err := c.get(ctx, path, parse)
		if err == nil {
			return m, status, nil
		}
		lastErr, lastStatus = err, status
		if status != 0 && status < http.StatusInternalServerError {
			// 4xx and parse errors are not retryable.
			return nil, status, err
		}
	}
	return nil, lastStatus, lastErr
}

// get issues a single GET request and decodes its body via parse.
func (c *LiteLLMClient) get(ctx context.Context, path string, parse func([]byte) (map[string]*bool, error)) (map[string]*bool, int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating model-info request: %w", err)
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching model info from %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading model-info body from %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("model-info endpoint %s returned %d", path, resp.StatusCode)
	}
	m, err := parse(body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return m, resp.StatusCode, nil
}
