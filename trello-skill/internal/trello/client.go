// Package trello implements a small Trello REST API client and the MCP tool
// handlers that expose Trello-backed todo operations to a FamClaw agent.
//
// This package is part of the trello-skill, an out-of-repo FamClaw addon. It
// is NOT compiled into the famclaw binary; the MCP server (cmd/trello-mcp)
// is launched at runtime by famclaw's MCP pool, reading credentials from the
// process environment (injected from skills.credentials in config.yaml).
package trello

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the Trello v1 REST API base.
const DefaultBaseURL = "https://api.trello.com/1"

// Env var names. These are injected by famclaw from skills.credentials; the
// credential map bypasses famclaw's env blocklist, so names ending in _TOKEN
// are safe to use here.
const (
	EnvAPIKey     = "TRELLO_API_KEY"
	EnvToken      = "TRELLO_TOKEN"
	EnvBoardID    = "TRELLO_BOARD_ID"
	EnvListID     = "TRELLO_LIST_ID"
	EnvDoneListID = "TRELLO_DONE_LIST_ID"
	EnvLists      = "TRELLO_LISTS"
)

// ErrNotConfigured is returned when Trello credentials are missing.
var ErrNotConfigured = fmt.Errorf("trello credentials not configured")

// Card is the subset of a Trello card we expose to the model.
type Card struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Desc             string `json:"desc"`
	IDList           string `json:"idList"`
	ShortLink        string `json:"shortLink"`
	ShortURL         string `json:"shortUrl"`
	DateLastActivity string `json:"dateLastActivity"`
	// Closed is true for cards moved to a closed/archived list. The idempotency
	// check only considers open cards, so closed duplicates don't block adds.
	Closed bool `json:"closed"`
}

// Client is the Trello API surface used by the MCP handlers.
type Client interface {
	// AddCard creates a card on listID with the given title. An empty listID
	// uses the configured default list. An empty description is allowed.
	AddCard(ctx context.Context, listID, title, description string) (Card, error)
	// ListCards returns the cards on listID (or the configured default list).
	ListCards(ctx context.Context, listID string) ([]Card, error)
	// CompleteCard moves cardID to the configured done list, marking it done.
	// cardID may be a Trello card ID or short link.
	CompleteCard(ctx context.Context, cardID string) (Card, error)
}

// Credentials holds the values read from the environment.
type Credentials struct {
	APIKey     string
	Token      string
	BoardID    string
	ListID     string
	DoneListID string
	// Lists is the name->list-id map parsed from the TRELLO_LISTS env var.
	// It drives name resolution and per-person routing. Empty when unset.
	Lists map[string]string
}

// LoadCredentials reads Trello credentials from the environment. It does NOT
// fail on missing values; validate them at call time so the MCP server can
// still start and surface a clear error per tool.
//
// TRELLO_LISTS is an optional JSON object mapping list/person names to Trello
// list ids, e.g. {"Backlog":"68e...","Julia":"68e...","Done":"68e..."}. It is
// the source of truth for name resolution and per-person routing — ids are
// never hardcoded in the skill. A malformed value disables name resolution
// (and is reported on stderr) but does not prevent the server from starting.
func LoadCredentials() Credentials {
	c := Credentials{
		APIKey:     firstNonEmpty(os.Getenv(EnvAPIKey), os.Getenv(EnvKeyFallback)),
		Token:      firstNonEmpty(os.Getenv(EnvToken), os.Getenv(EnvTokenFallback)),
		BoardID:    os.Getenv(EnvBoardID),
		ListID:     os.Getenv(EnvListID),
		DoneListID: os.Getenv(EnvDoneListID),
	}
	if raw := os.Getenv(EnvLists); raw != "" {
		var lists map[string]string
		if err := json.Unmarshal([]byte(raw), &lists); err != nil {
			fmt.Fprintf(os.Stderr, "trello-skill: %s is not valid JSON (%v); list name resolution disabled\n", EnvLists, err)
		} else {
			c.Lists = lists
		}
	}
	return c
}

// EnvKeyFallback is an alternative env var name some deployments use.
const EnvKeyFallback = "TRELLO_KEY"

// EnvTokenFallback is an alternative env var name some deployments use.
const EnvTokenFallback = "TRELLO_OAUTH_TOKEN"

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// HTTPClient implements Client via the Trello REST API.
type HTTPClient struct {
	baseURL    string
	creds      Credentials
	httpClient *http.Client
}

// NewHTTPClient builds a Client from credentials. A nil creds yields a client
// whose every call returns ErrNotConfigured, so the MCP server can boot even
// before the captain supplies live Trello secrets.
func NewHTTPClient(creds Credentials) *HTTPClient {
	return &HTTPClient{
		baseURL: DefaultBaseURL,
		creds:   creds,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// withBaseURL lets tests redirect requests to a mock server.
func (c *HTTPClient) withBaseURL(url string) *HTTPClient {
	cc := *c
	cc.baseURL = strings.TrimRight(url, "/")
	return &cc
}

func (c *HTTPClient) validate() error {
	if c.creds.APIKey == "" || c.creds.Token == "" {
		return ErrNotConfigured
	}
	return nil
}

// AddCard creates a card.
func (c *HTTPClient) AddCard(ctx context.Context, listID, title, description string) (Card, error) {
	if err := c.validate(); err != nil {
		return Card{}, err
	}
	if strings.TrimSpace(title) == "" {
		return Card{}, fmt.Errorf("cannot add a card with an empty title")
	}
	if listID == "" {
		listID = c.creds.ListID
	}
	if listID == "" {
		return Card{}, fmt.Errorf("no list ID provided and TRELLO_LIST_ID not configured")
	}
	v := url.Values{}
	v.Set("key", c.creds.APIKey)
	v.Set("token", c.creds.Token)
	v.Set("idList", listID)
	v.Set("name", title)
	if description != "" {
		v.Set("desc", description)
	}
	body, err := c.doPost(ctx, "/cards", v)
	if err != nil {
		return Card{}, fmt.Errorf("creating card: %w", err)
	}
	var card Card
	if err := json.Unmarshal(body, &card); err != nil {
		return Card{}, fmt.Errorf("parsing created card: %w", err)
	}
	return card, nil
}

// ListCards lists cards on a list.
func (c *HTTPClient) ListCards(ctx context.Context, listID string) ([]Card, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if listID == "" {
		listID = c.creds.ListID
	}
	if listID == "" {
		return nil, fmt.Errorf("no list ID provided and TRELLO_LIST_ID not configured")
	}
	v := url.Values{}
	v.Set("key", c.creds.APIKey)
	v.Set("token", c.creds.Token)
	u := c.baseURL + "/lists/" + url.PathEscape(listID) + "/cards?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("building list request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing cards: %w", err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading list response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var cards []Card
	if err := json.Unmarshal(body, &cards); err != nil {
		return nil, fmt.Errorf("parsing card list: %w", err)
	}
	return cards, nil
}

// CompleteCard moves cardID to the configured done list.
func (c *HTTPClient) CompleteCard(ctx context.Context, cardID string) (Card, error) {
	if err := c.validate(); err != nil {
		return Card{}, err
	}
	if strings.TrimSpace(cardID) == "" {
		return Card{}, fmt.Errorf("card_id is required")
	}
	if c.creds.DoneListID == "" {
		return Card{}, fmt.Errorf("cannot complete a card: TRELLO_DONE_LIST_ID not configured")
	}
	v := url.Values{}
	v.Set("key", c.creds.APIKey)
	v.Set("token", c.creds.Token)
	v.Set("idList", c.creds.DoneListID)
	v.Set("pos", "top")
	u := c.baseURL + "/cards/" + url.PathEscape(cardID) + "?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return Card{}, fmt.Errorf("building move request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Card{}, fmt.Errorf("moving card: %w", err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp.Body)
	if err != nil {
		return Card{}, fmt.Errorf("reading move response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Card{}, fmt.Errorf("trello API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var card Card
	if err := json.Unmarshal(body, &card); err != nil {
		return Card{}, fmt.Errorf("parsing moved card: %w", err)
	}
	return card, nil
}

func (c *HTTPClient) doPost(ctx context.Context, path string, v url.Values) ([]byte, error) {
	u := c.baseURL + path + "?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("trello API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// readAll is a seam over io.ReadAll so tests can stub body reading.
var readAll = io.ReadAll
