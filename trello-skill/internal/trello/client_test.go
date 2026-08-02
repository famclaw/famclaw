package trello

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newMockTrello starts an httptest server with the given handler and returns
// it. The server is closed automatically when the test ends.
func newMockTrello(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fn))
	t.Cleanup(srv.Close)
	return srv
}

func clientAt(url string) *HTTPClient {
	return newClient(Credentials{APIKey: "key", Token: "tok", ListID: "list1", DoneListID: "done1"}, url)
}

func newClient(creds Credentials, url string) *HTTPClient {
	return NewHTTPClient(creds).withBaseURL(url)
}

func TestAddCard(t *testing.T) {
	tests := []struct {
		name        string
		creds       Credentials
		listID      string
		title       string
		desc        string
		wantPath    string
		wantErr     bool
		errContains string
		wantName    string
		wantID      string
		wantLink    string
	}{
		{
			name:     "success full body",
			creds:    Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			listID:   "",
			title:    "Buy milk",
			desc:     "2%",
			wantPath: "/cards",
			wantName: "Buy milk",
			wantID:   "abc123",
			wantLink: "abc123",
		},
		{
			name:        "empty title rejected",
			creds:       Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			title:       "  ",
			wantErr:     true,
			errContains: "empty title",
		},
		{
			name:        "no creds",
			creds:       Credentials{},
			title:       "x",
			wantErr:     true,
			errContains: "not configured",
		},
		{
			name:        "no list id",
			creds:       Credentials{APIKey: "k", Token: "t"},
			title:       "x",
			wantErr:     true,
			errContains: "TRELLO_LIST_ID",
		},
		{
			name:     "explicit list override",
			creds:    Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			listID:   "L2",
			title:    "task",
			wantPath: "/cards",
			wantName: "task",
			wantID:   "x9",
			wantLink: "x9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotQuery url.Values
			srv := newMockTrello(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(Card{
					ID: tt.wantID, Name: tt.wantName, ShortLink: tt.wantLink,
					IDList: tt.creds.ListID, ShortURL: "https://trello.com/c/" + tt.wantLink,
				})
			})

			c := newClient(tt.creds, srv.URL)
			card, err := c.AddCard(context.Background(), tt.listID, tt.title, tt.desc)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if card.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", card.ID, tt.wantID)
			}
			if card.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", card.Name, tt.wantName)
			}
			// Verify HTTP request shape.
			if tt.wantPath != "" && gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotQuery.Get("key") != tt.creds.APIKey {
				t.Errorf("query key = %q, want %q", gotQuery.Get("key"), tt.creds.APIKey)
			}
			if gotQuery.Get("token") != tt.creds.Token {
				t.Errorf("query token = %q, want %q", gotQuery.Get("token"), tt.creds.Token)
			}
			if gotQuery.Get("name") != tt.title {
				t.Errorf("query name = %q, want %q", gotQuery.Get("name"), tt.title)
			}
			if tt.desc != "" && gotQuery.Get("desc") != tt.desc {
				t.Errorf("query desc = %q, want %q", gotQuery.Get("desc"), tt.desc)
			}
		})
	}
}

func TestAddCardRequestMethodAndList(t *testing.T) {
	var gotMethod, gotList string
	srv := newMockTrello(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotList = r.URL.Query().Get("idList")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"c1","name":"t","shortLink":"c1","idList":"L2","shortUrl":"u"}`)
	})

	c := newClient(Credentials{APIKey: "k", Token: "t", ListID: "L1"}, srv.URL)
	if _, err := c.AddCard(context.Background(), "L2", "t", ""); err != nil {
		t.Fatalf("AddCard: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotList != "L2" {
		t.Errorf("idList = %q, want L2 (arg should override default)", gotList)
	}
}

func TestListCards(t *testing.T) {
	tests := []struct {
		name        string
		creds       Credentials
		listID      string
		wantPath    string
		payload     string
		wantCount   int
		wantErr     bool
		errContains string
	}{
		{
			name:      "success multiple cards",
			creds:     Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			listID:    "",
			wantPath:  "/lists/L1/cards",
			payload:   `[{"id":"a","name":"one","shortLink":"a","shortUrl":"ua"},{"id":"b","name":"two","shortLink":"b","shortUrl":"ub"}]`,
			wantCount: 2,
		},
		{
			name:      "empty list",
			creds:     Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			payload:   `[]`,
			wantCount: 0,
		},
		{
			name:      "explicit list override",
			creds:     Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			listID:    "L3",
			wantPath:  "/lists/L3/cards",
			payload:   `[]`,
			wantCount: 0,
		},
		{
			name:        "no creds",
			creds:       Credentials{},
			listID:      "L1",
			wantErr:     true,
			errContains: "not configured",
		},
		{
			name:        "no list id",
			creds:       Credentials{APIKey: "k", Token: "t"},
			listID:      "",
			wantErr:     true,
			errContains: "TRELLO_LIST_ID",
		},
		{
			name:        "api error",
			creds:       Credentials{APIKey: "k", Token: "t", ListID: "L1"},
			listID:      "L1",
			payload:     ``,
			wantErr:     true,
			errContains: "trello API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := newMockTrello(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				if tt.name == "api error" {
					w.WriteHeader(http.StatusUnauthorized)
					io.WriteString(w, "bad token")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, tt.payload)
			})

			c := newClient(tt.creds, srv.URL)
			cards, err := c.ListCards(context.Background(), tt.listID)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cards) != tt.wantCount {
				t.Errorf("got %d cards, want %d", len(cards), tt.wantCount)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("method = %q, want GET", gotMethod)
			}
			if tt.wantPath != "" && gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestCompleteCard(t *testing.T) {
	tests := []struct {
		name        string
		creds       Credentials
		cardID      string
		wantPath    string
		wantIDList  string
		payload     string
		wantErr     bool
		errContains string
	}{
		{
			name:       "success",
			creds:      Credentials{APIKey: "k", Token: "t", DoneListID: "done1"},
			cardID:     "abc",
			wantPath:   "/cards/abc",
			wantIDList: "done1",
			payload:    `{"id":"abc","name":"done task","shortLink":"abc","idList":"done1","shortUrl":"u"}`,
		},
		{
			name:        "no creds",
			creds:       Credentials{},
			cardID:      "abc",
			wantErr:     true,
			errContains: "not configured",
		},
		{
			name:        "empty card id",
			creds:       Credentials{APIKey: "k", Token: "t", DoneListID: "done1"},
			cardID:      "  ",
			wantErr:     true,
			errContains: "card_id is required",
		},
		{
			name:        "no done list",
			creds:       Credentials{APIKey: "k", Token: "t"},
			cardID:      "abc",
			wantErr:     true,
			errContains: "TRELLO_DONE_LIST_ID",
		},
		{
			name:       "builds path from card id",
			creds:      Credentials{APIKey: "k", Token: "t", DoneListID: "done1"},
			cardID:     "abc-def",
			wantPath:   "/cards/abc-def",
			wantIDList: "done1",
			payload:    `{"id":"abc-def","name":"x","shortLink":"x","idList":"done1","shortUrl":"u"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath, gotList string
			srv := newMockTrello(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotList = r.URL.Query().Get("idList")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, tt.payload)
			})

			c := newClient(tt.creds, srv.URL)
			card, err := c.CompleteCard(context.Background(), tt.cardID)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMethod != http.MethodPut {
				t.Errorf("method = %q, want PUT", gotMethod)
			}
			if tt.wantPath != "" && gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if tt.wantIDList != "" && gotList != tt.wantIDList {
				t.Errorf("idList = %q, want %q", gotList, tt.wantIDList)
			}
			if card.IDList != tt.wantIDList {
				t.Errorf("card.IDList = %q, want %q", card.IDList, tt.wantIDList)
			}
		})
	}
}

func TestLoadCredentials(t *testing.T) {
	t.Setenv(EnvAPIKey, "fromkey")
	t.Setenv(EnvToken, "fromtok")
	t.Setenv(EnvBoardID, "b1")
	t.Setenv(EnvListID, "l1")
	t.Setenv(EnvDoneListID, "d1")
	creds := LoadCredentials()
	if creds.APIKey != "fromkey" {
		t.Errorf("APIKey = %q", creds.APIKey)
	}
	if creds.Token != "fromtok" {
		t.Errorf("Token = %q", creds.Token)
	}
	if creds.BoardID != "b1" {
		t.Errorf("BoardID = %q", creds.BoardID)
	}
	if creds.ListID != "l1" {
		t.Errorf("ListID = %q", creds.ListID)
	}
	if creds.DoneListID != "d1" {
		t.Errorf("DoneListID = %q", creds.DoneListID)
	}
}

func TestLoadCredentialsLists(t *testing.T) {
	t.Setenv(EnvAPIKey, "k")
	t.Setenv(EnvToken, "t")
	t.Setenv(EnvListID, "def1234567890abcdef12")

	t.Run("valid JSON parsed", func(t *testing.T) {
		t.Setenv(EnvLists, `{"Backlog":"aaaa1111aaaa1111aaaa1111","Julia":"bbbb2222bbbb2222bbbb2222"}`)
		creds := LoadCredentials()
		if creds.Lists == nil {
			t.Fatal("expected Lists to be parsed")
		}
		if creds.Lists["Backlog"] != "aaaa1111aaaa1111aaaa1111" {
			t.Errorf("Backlog = %q", creds.Lists["Backlog"])
		}
		if creds.Lists["Julia"] != "bbbb2222bbbb2222bbbb2222" {
			t.Errorf("Julia = %q", creds.Lists["Julia"])
		}
	})

	t.Run("malformed JSON disables lists", func(t *testing.T) {
		t.Setenv(EnvLists, `{not json`)
		creds := LoadCredentials()
		if creds.Lists != nil {
			t.Errorf("expected Lists to be nil on malformed JSON, got %v", creds.Lists)
		}
		if creds.APIKey != "k" {
			t.Errorf("APIKey should still load: %q", creds.APIKey)
		}
	})

	t.Run("unset leaves Lists nil", func(t *testing.T) {
		t.Setenv(EnvLists, "")
		creds := LoadCredentials()
		if creds.Lists != nil {
			t.Errorf("expected nil Lists, got %v", creds.Lists)
		}
	})
}

func TestListCardsParsesClosed(t *testing.T) {
	payload := `[{"id":"a","name":"open","shortLink":"a","shortUrl":"ua"},
{"id":"b","name":"done","shortLink":"b","shortUrl":"ub","closed":true}]`
	srv := newMockTrello(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, payload)
	})
	c := newClient(Credentials{APIKey: "k", Token: "t", ListID: "L1"}, srv.URL)
	cards, err := c.ListCards(context.Background(), "")
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if cards[0].Closed {
		t.Errorf("open card reported closed")
	}
	if !cards[1].Closed {
		t.Errorf("closed card reported open")
	}
}

func TestNewResolverFromCredentials(t *testing.T) {
	r := NewResolver(Credentials{
		ListID: "def1234567890abcdef12",
		Lists:  map[string]string{"Backlog": "aaa111aaa111aaa111aaa111", "Done": "ddd111ddd111ddd111ddd111"},
	})
	if r.DefaultListID != "def1234567890abcdef12" {
		t.Errorf("DefaultListID = %q", r.DefaultListID)
	}
	if r.DoneListID != "ddd111ddd111ddd111ddd111" {
		t.Errorf("DoneListID = %q", r.DoneListID)
	}
}
