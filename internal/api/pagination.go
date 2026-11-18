package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

// Pagination limits for every list endpoint.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 100
)

// Page is the body of every list response:
//
//	{"data": [...], "next_cursor": "..." | null}
//
// next_cursor is null on the last page. Pass it back as ?cursor= for the next.
type Page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// PageRequest is a parsed ?limit=&cursor= pair.
type PageRequest struct {
	Limit int
	// After is the keyset value of the last row on the previous page, or ""
	// for the first page. What it means is up to the list (usually a row ID,
	// or a sort value and ID joined by the handler).
	After string
}

// cursor is what an opaque cursor string decodes to. Cursors are base64url
// JSON so a handler can change what's inside later without breaking the format.
type cursor struct {
	After string `json:"a"`
}

// EncodeCursor returns the opaque cursor for keyset value after.
func EncodeCursor(after string) string {
	b, _ := json.Marshal(cursor{After: after})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(s string) (string, error) {
	invalid := BadRequest("invalid_cursor", "The cursor is not one this API returned.").
		WithHint("Pass next_cursor from the previous page unchanged, or leave cursor out for the first page.")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", invalid
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.After == "" {
		return "", invalid
	}
	return c.After, nil
}

// ParsePageRequest reads ?limit= (1 to MaxPageLimit, default
// DefaultPageLimit) and ?cursor=.
func ParsePageRequest(r *http.Request) (PageRequest, error) {
	q := r.URL.Query()
	pr := PageRequest{Limit: DefaultPageLimit}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxPageLimit {
			return pr, BadRequest("invalid_limit", "limit must be a whole number from 1 to "+strconv.Itoa(MaxPageLimit)+".")
		}
		pr.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		after, err := DecodeCursor(v)
		if err != nil {
			return pr, err
		}
		pr.After = after
	}
	return pr, nil
}

// NewPage builds a page from rows fetched with LIMIT pr.Limit+1, in keyset
// order. The extra row only says whether there's a next page; it's dropped,
// and the cursor points after the last row kept. key returns a row's keyset
// value, which must strictly increase in the list's order.
func NewPage[T any](rows []T, limit int, key func(T) string) Page[T] {
	p := Page[T]{Data: rows}
	if p.Data == nil {
		p.Data = []T{}
	}
	if len(rows) > limit {
		p.Data = rows[:limit]
		c := EncodeCursor(key(p.Data[limit-1]))
		p.NextCursor = &c
	}
	return p
}
