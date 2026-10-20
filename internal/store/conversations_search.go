package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ConversationSearchHit is one FTS5 match returned by SearchConversations.
type ConversationSearchHit struct {
	ID             int64
	ConversationID string
	Role           string
	CreatedAt      time.Time
	Snippet        string
}

const (
	// searchConversationsDefaultMax is the default result cap.
	searchConversationsDefaultMax = 10
	// searchConversationsHardMax is the server-side hard cap (the clamp is
	// enforced here, never left to the documented hint).
	searchConversationsHardMax = 25
)

// SearchConversations performs a user-scoped FTS5 keyword search over the
// caller's own messages (Tier 1 conversation recall).
//
// Scoping is enforced server-side: userName comes from the agent session
// (see the dispatch layer) and is never taken from LLM-supplied arguments,
// so a call can only ever return that user's own messages.
//
// query is escaped per FTS5 rules (see fts5MatchExpr) before it reaches
// MATCH. If the primary expression still fails with an FTS5 syntax error,
// an all-terms-quoted OR fallback is attempted; a syntax error is never
// surfaced to the caller. A query with no usable tokens returns an empty
// result rather than an error.
//
// fromDate/toDate are optional inclusive YYYY-MM-DD bounds on
// date(created_at); unparseable bounds are ignored (treated as unset) so a
// malformed LLM argument degrades to "no bound" instead of a query error.
//
// Results are ordered by BM25 relevance and capped server-side: maxResults
// is clamped to [1,25], defaulting to 10 when not positive.
func (d *DB) SearchConversations(ctx context.Context, userName, query, fromDate, toDate string, maxResults int) ([]ConversationSearchHit, error) {
	if userName == "" {
		return nil, fmt.Errorf("search conversations: userName is required")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search conversations: query is required")
	}
	if maxResults <= 0 {
		maxResults = searchConversationsDefaultMax
	}
	if maxResults > searchConversationsHardMax {
		maxResults = searchConversationsHardMax
	}

	match := fts5MatchExpr(query)
	if match == "" {
		// No searchable tokens survived escaping: nothing to match.
		return []ConversationSearchHit{}, nil
	}

	q := `SELECT m.id, m.conversation_id, m.role, m.created_at,
	          snippet(messages_fts, 0, '[', ']', '...', 40)
	         FROM messages_fts
	         JOIN messages m ON m.id = messages_fts.rowid
	         JOIN conversations c ON c.id = m.conversation_id
	        WHERE messages_fts MATCH ? AND c.user_name = ?`
	args := []any{match, userName}
	if from := normalizeSearchDate(fromDate); from != "" {
		q += " AND date(m.created_at) >= ?"
		args = append(args, from)
	}
	if to := normalizeSearchDate(toDate); to != "" {
		q += " AND date(m.created_at) <= ?"
		args = append(args, to)
	}
	q += " ORDER BY bm25(messages_fts) LIMIT ?"
	args = append(args, maxResults)

	hits, err := d.queryConversationSearch(ctx, q, args...)
	if err != nil && fts5IsSyntaxError(err) {
		// Last-resort safety net: the primary expression is built to be
		// syntactically valid for any input, so reaching this path means
		// something unexpected — retry with an all-terms-quoted OR
		// expression instead of surfacing the syntax error.
		fallback := fts5FallbackExpr(query)
		if fallback == "" {
			return []ConversationSearchHit{}, nil
		}
		args[0] = fallback
		hits, err = d.queryConversationSearch(ctx, q, args...)
	}
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// queryConversationSearch runs the (caller-built) FTS search query.
func (d *DB) queryConversationSearch(ctx context.Context, q string, args ...any) ([]ConversationSearchHit, error) {
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search conversations: %w", err)
	}
	defer rows.Close()
	var hits []ConversationSearchHit
	for rows.Next() {
		var h ConversationSearchHit
		if err := rows.Scan(&h.ID, &h.ConversationID, &h.Role, &h.CreatedAt, &h.Snippet); err != nil {
			return nil, fmt.Errorf("scan conversation search row: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation search rows: %w", err)
	}
	return hits, nil
}

// normalizeSearchDate validates an inclusive date bound (YYYY-MM-DD);
// anything unparseable is dropped (treated as no bound).
func normalizeSearchDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return ""
	}
	return s
}

// fts5IsSyntaxError reports whether err is an FTS5 MATCH syntax failure.
func fts5IsSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "malformed MATCH") || strings.Contains(msg, "fts5: syntax")
}

// fts5SyntaxChars are the characters with special meaning in an FTS5 MATCH
// expression (operators, phrase quotes, prefix star, boost caret, grouping
// parens, column-filter colon, string-literal backslash, and other
// punctuation that can form an operator token). They are trimmed from the
// ends of bare tokens so the remainder is re-quoted and matched as plain
// text; interior characters are left alone so ordinary hyphenated words
// (e.g. "mother-in-law") keep their natural tokenization.
const fts5SyntaxChars = `"*-^()[]{},;:!?@#$%&|` + "`" + `~=` + `\\`

// fts5MatchExpr builds a safe FTS5 MATCH expression from raw query text.
//
//   - A run wrapped in double quotes is kept as an exact phrase.
//   - Any other whitespace-separated token has fts5SyntaxChars trimmed from
//     its ends and is re-quoted as a literal; tokens that are empty after
//     trimming are dropped.
//   - Remaining phrases/tokens are AND-joined.
//
// The returned expression is syntactically valid FTS5 for any input. An
// empty string means no usable tokens (the caller treats that as
// "no matches" without running a query).
func fts5MatchExpr(raw string) string {
	var parts []string
	rest := raw
	for {
		q := strings.IndexByte(rest, '"')
		if q < 0 {
			parts = append(parts, fts5LiteralTokens(rest)...)
			break
		}
		if q > 0 {
			parts = append(parts, fts5LiteralTokens(rest[:q])...)
		}
		rest = rest[q:]
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			// Unbalanced quote: treat the remainder as ordinary tokens.
			parts = append(parts, fts5LiteralTokens(rest)...)
			break
		}
		phrase := strings.TrimSpace(rest[1 : 1+end])
		rest = rest[1+end+1:]
		if phrase != "" {
			parts = append(parts, `"`+phrase+`"`)
		}
	}
	return strings.Join(parts, " AND ")
}

// fts5FallbackExpr is the last-resort MATCH expression: every whitespace
// token trimmed of FTS5 syntax characters, quoted, and OR-joined.
func fts5FallbackExpr(raw string) string {
	var parts []string
	for _, tok := range strings.Fields(raw) {
		clean := strings.Trim(tok, fts5SyntaxChars)
		if clean != "" {
			parts = append(parts, `"`+clean+`"`)
		}
	}
	return strings.Join(parts, " OR ")
}

// fts5LiteralTokens splits s on whitespace and returns each surviving
// token re-quoted as an FTS5 literal.
func fts5LiteralTokens(s string) []string {
	var out []string
	for _, tok := range strings.Fields(s) {
		clean := strings.Trim(tok, fts5SyntaxChars)
		if clean != "" {
			out = append(out, `"`+clean+`"`)
		}
	}
	return out
}
