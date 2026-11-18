package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/version"
)

// apiErrorBody is the API's error shape.
type apiErrorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Hint    string         `json:"hint"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// call sends one API request and decodes a 2xx JSON response into out (which
// may be nil). An error response becomes an *Error with the API's code,
// message and hint and the matching exit code. auth says whether the request
// needs the API key or login token.
func (a *App) call(ctx context.Context, method, path string, body, out any, auth bool) error {
	s, err := a.settings()
	if err != nil {
		return err
	}
	token := ""
	if auth {
		if s.APIKey == "" {
			return authError("Not signed in.", "Run `mailgoose login`, or set "+EnvAPIKey+" to an API key (agents and scripts).")
		}
		token = s.APIKey
	}
	_, err = a.send(ctx, s.APIURL, method, path, body, out, token)
	return err
}

// send makes one request to apiURL with token (none if empty) and returns the
// status of a 2xx response.
func (a *App) send(ctx context.Context, apiURL, method, path string, body, out any, token string) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "mailgoose-cli/"+version.Get().Version)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return 0, &Error{Exit: ExitError, Code: "unreachable", Message: fmt.Sprintf("Cannot reach %s: %v", apiURL, err),
			Hint: "Check --api-url or " + EnvAPIURL + ", and your network."}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read response from %s: %w", apiURL, err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(raw) == 0 {
			return resp.StatusCode, nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, fmt.Errorf("the API sent a response the CLI can't read: %w", err)
		}
		return resp.StatusCode, nil
	}
	return 0, responseError(resp, raw)
}

// responseError maps an error response to an *Error. The exit code follows
// the status, except that an API code of confirmation_required always means
// exit 5, whatever its status.
func responseError(resp *http.Response, raw []byte) *Error {
	var body apiErrorBody
	_ = json.Unmarshal(raw, &body)
	e := &Error{Code: body.Error.Code, Message: body.Error.Message, Hint: body.Error.Hint, Details: body.Error.Details}
	if e.Code == "" {
		e.Code = "http_" + fmt.Sprint(resp.StatusCode)
	}
	if e.Message == "" {
		e.Message = "The API answered " + resp.Status + "."
	}
	if rid := resp.Header.Get("X-Request-Id"); rid != "" && resp.StatusCode >= 500 && e.Hint == "" {
		e.Hint = "Request ID " + rid + "."
	}
	switch {
	case e.Code == "confirmation_required":
		e.Exit = ExitNeedsConfirmation
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.Exit = ExitAuth
	case resp.StatusCode == http.StatusNotFound:
		e.Exit = ExitNotFound
	default:
		e.Exit = ExitError
	}
	return e
}
