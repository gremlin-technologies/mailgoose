package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error is an API error: an HTTP status plus the body every error response
// carries. Handlers return or write these; nothing else writes error bodies.
type Error struct {
	Status int
	// Code is stable and machine-readable; clients and the CLI branch on it.
	Code string
	// Message says what went wrong, for a person.
	Message string
	// Hint says what to do about it. It may be empty.
	Hint string
	// Details are extra facts some errors carry, such as the limit and count
	// of a contact_limit. Usually nil.
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// WithDetails returns a copy of e with its details set.
func (e *Error) WithDetails(d map[string]any) *Error {
	c := *e
	c.Details = d
	return &c
}

// WithHint returns a copy of e with its hint set.
func (e *Error) WithHint(hint string) *Error {
	c := *e
	c.Hint = hint
	return &c
}

// ErrorBody is the shape of every error response:
//
//	{"error": {"code": "...", "message": "...", "hint": "..."}}
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inside of ErrorBody. Hint is always present, and empty
// when there's nothing to add.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	// Details is left out when empty.
	Details map[string]any `json:"details,omitempty"`
}

// Constructors for the statuses the API uses. The code is the caller's, so
// it can say exactly what failed (such as "invalid_cursor" for a 400).

// BadRequest is a 400: the request is malformed.
func BadRequest(code, msg string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: code, Message: msg}
}

// Unauthorized is a 401: no valid credentials.
func Unauthorized(code, msg string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: code, Message: msg}
}

// Forbidden is a 403: valid credentials that may not do this.
func Forbidden(code, msg string) *Error {
	return &Error{Status: http.StatusForbidden, Code: code, Message: msg}
}

// NotFound is a 404.
func NotFound(code, msg string) *Error {
	return &Error{Status: http.StatusNotFound, Code: code, Message: msg}
}

// Conflict is a 409: the request clashes with the current state.
func Conflict(code, msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: msg}
}

// Unprocessable is a 422: well-formed, but the values are not acceptable.
func Unprocessable(code, msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: code, Message: msg}
}

// TooManyRequests is a 429.
func TooManyRequests(code, msg string) *Error {
	return &Error{Status: http.StatusTooManyRequests, Code: code, Message: msg}
}

// Internal is a 500. Its message never carries the underlying error, which
// goes to the log instead.
func Internal() *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "Something went wrong on our side.",
		Hint: "Try again. If it keeps happening, contact support with the X-Request-Id header."}
}

// WriteError writes err as an error response. An error that isn't an *Error
// becomes a 500, and is never shown to the client.
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Internal()
	}
	writeJSON(w, e.Status, ErrorBody{Error: ErrorDetail{Code: e.Code, Message: e.Message, Hint: e.Hint, Details: e.Details}})
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	WriteError(w, &Error{Status: status, Code: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteJSON writes v as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// MaxBodyBytes is the largest JSON request body handlers accept.
const MaxBodyBytes = 1 << 20

// DecodeJSON reads the request body into v. Unknown fields, trailing data and
// bodies over MaxBodyBytes are errors, so typos in field names surface as 400s
// rather than being silently ignored.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSON(w, r, v, MaxBodyBytes)
}

// decodeJSON is DecodeJSON with its own body limit.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return BadRequest("unsupported_content_type", "Send the request body as JSON.").
			WithHint("Set Content-Type: application/json.")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large",
				Message: fmt.Sprintf("The request body is over %d bytes.", maxBytes)}
		case errors.Is(err, io.EOF):
			return BadRequest("invalid_json", "The request body is empty.").WithHint("Send a JSON object.")
		default:
			return BadRequest("invalid_json", "The request body is not valid JSON: "+err.Error())
		}
	}
	if dec.More() {
		return BadRequest("invalid_json", "The request body has data after the JSON value.")
	}
	return nil
}
