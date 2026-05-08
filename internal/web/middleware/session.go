// Package middleware provides HTTP middleware for the web UI, including
// session validation. WithSession is the gate that turns an authenticated
// session cookie into an Identity attached to the request context; routes
// downstream of this middleware can assume the request is authenticated.
package middleware

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/famclaw/famclaw/internal/store"
)

// ctxKey is an unexported type used to namespace session values in
// context.Value lookups, preventing collisions with other packages.
type ctxKey int

const sessionCtxKey ctxKey = iota

// Identity is the authenticated principal extracted from a valid session
// cookie. It is attached to the request context by WithSession and can be
// retrieved by downstream handlers via IdentityFrom.
type Identity struct {
	SessionID string
	UserID    int64
}

// WithSession returns an http.Handler middleware that validates the
// "famclaw_session" cookie on every request. On a valid hit it attaches an
// Identity to the request context, fires a best-effort Touch in a goroutine
// to refresh last_seen, and calls the next handler. On a miss (no cookie,
// invalid cookie, or expired session) it short-circuits with 401 JSON.
//
// The Touch goroutine intentionally uses context.Background() with a 5-second
// timeout rather than r.Context(): the request context is cancelled the
// moment the response is written, which would race the UPDATE and leave
// last_seen stale.
func WithSession(sessions *store.SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("famclaw_session")
			if err != nil || cookie.Value == "" {
				writeUnauth(w)
				return
			}

			sess, err := sessions.Get(r.Context(), cookie.Value)
			if errors.Is(err, store.ErrNoSession) {
				writeUnauth(w)
				return
			}
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			// Refresh last_seen in the background. Use a fresh context so the
			// UPDATE is not cancelled when the response handler returns.
			go func() {
				ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = sessions.Touch(ctx2, sess.ID)
			}()

			ctx := context.WithValue(r.Context(), sessionCtxKey, &Identity{
				SessionID: sess.ID,
				UserID:    sess.UserID,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IdentityFrom extracts the Identity attached by WithSession. The bool result
// is false when the context has no Identity (i.e. the handler ran without
// the WithSession middleware in front of it).
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	v, ok := ctx.Value(sessionCtxKey).(*Identity)
	return v, ok
}

// writeUnauth writes the canonical 401 JSON body. The trailing newline keeps
// curl/jq output well-formatted and matches the rest of the web package.
func writeUnauth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthenticated"}` + "\n"))
}
