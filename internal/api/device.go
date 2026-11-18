package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// devicePagePath is the web app's approval page (web/src/pages/Device.tsx).
const devicePagePath = "/device"

// CreateDeviceLogin is POST /v1/device-logins.
func (s *Server) CreateDeviceLogin(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.CreateDeviceLoginRequest
	if r.ContentLength != 0 {
		if err := DecodeJSON(w, r, &req); err != nil {
			WriteError(w, err)
			return
		}
	}
	name := ""
	if req.ClientName != nil {
		name = *req.ClientName
	}
	start, err := s.Auth.StartDeviceLogin(r.Context(), name)
	if s.authError(w, r, err) {
		return
	}
	page := s.webURL(r) + devicePagePath
	writeJSON(w, http.StatusCreated, apigen.DeviceLoginStart{
		DeviceCode:              start.DeviceCode,
		UserCode:                start.UserCode,
		VerificationUrl:         page,
		VerificationUrlComplete: page + "?code=" + url.QueryEscape(start.UserCode),
		ExpiresIn:               int(auth.DeviceLoginTTL.Seconds()),
		Interval:                int(start.Interval.Seconds()),
	})
}

// PollDeviceLogin is POST /v1/device-logins/token.
func (s *Server) PollDeviceLogin(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.PollDeviceLoginRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	interval := int(auth.DevicePollInterval.Seconds())
	t, err := s.Auth.PollDeviceLogin(r.Context(), req.DeviceCode)
	switch {
	case errors.Is(err, auth.ErrDeviceLoginPending):
		writeJSON(w, http.StatusAccepted, apigen.DeviceLoginPending{Status: apigen.DeviceLoginPendingStatusPending, Interval: interval})
		return
	case errors.Is(err, auth.ErrDeviceLoginSlowDown):
		w.Header().Set("Retry-After", strconv.Itoa(interval))
		WriteError(w, TooManyRequests("slow_down", "Polling too often.").
			WithHint("Wait "+strconv.Itoa(interval)+" seconds between polls."))
		return
	case errors.Is(err, auth.ErrDeviceLoginDenied):
		WriteError(w, Forbidden("device_login_denied", "The login was denied in the browser.").
			WithHint("Run `mailgoose login` again if that was a mistake."))
		return
	case errors.Is(err, auth.ErrDeviceLoginExpired):
		WriteError(w, &Error{Status: http.StatusGone, Code: "device_login_expired",
			Message: "This login request has expired, doesn't exist or was already used.",
			Hint:    "Run `mailgoose login` again; a request lasts 10 minutes."})
		return
	}
	if s.authError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.DeviceLoginToken{
		Token:   t.Token.Secret,
		KeyId:   t.Token.ID,
		Account: accountSummaryJSON(t.Account),
		User:    userJSON(t.User),
	})
}

// GetDeviceLogin is GET /v1/device-logins/{user_code}.
func (s *Server) GetDeviceLogin(w http.ResponseWriter, r *http.Request, userCode string) {
	if _, ok := s.currentSession(w, r); !ok {
		return
	}
	d, err := s.Auth.PendingDeviceLogin(r.Context(), userCode)
	if s.deviceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.DeviceLogin{
		UserCode: d.UserCode, ClientName: d.ClientName, CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
	})
}

// ApproveDeviceLogin is POST /v1/device-logins/{user_code}/approve.
func (s *Server) ApproveDeviceLogin(w http.ResponseWriter, r *http.Request, userCode string) {
	s.decideDeviceLogin(w, r, userCode, true)
}

// DenyDeviceLogin is POST /v1/device-logins/{user_code}/deny.
func (s *Server) DenyDeviceLogin(w http.ResponseWriter, r *http.Request, userCode string) {
	s.decideDeviceLogin(w, r, userCode, false)
}

func (s *Server) decideDeviceLogin(w http.ResponseWriter, r *http.Request, userCode string, approve bool) {
	cur, ok := s.currentSession(w, r)
	if !ok {
		return
	}
	err := s.Auth.DecideDeviceLogin(r.Context(), userCode, cur.Account.ID, cur.User.ID, approve)
	if s.deviceError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deviceError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, auth.ErrDeviceLoginNotFound) {
		WriteError(w, NotFound("not_found", "No login request with that code is waiting for approval.").
			WithHint("Check the code in your terminal. Requests expire after 10 minutes; run `mailgoose login` again if needed."))
		return true
	}
	return s.authError(w, r, err)
}

// RevokeCurrentCliToken is DELETE /v1/cli-tokens/current.
func (s *Server) RevokeCurrentCliToken(w http.ResponseWriter, r *http.Request) {
	c := s.requireCaller(w, r)
	if c == nil {
		return
	}
	if c.Kind != auth.KindCLIToken {
		WriteError(w, BadRequest("not_a_cli_token", "This request wasn't made with a CLI token, so there's nothing to sign out.").
			WithHint("Revoke API keys with `mailgoose keys revoke`."))
		return
	}
	err := s.Auth.RevokeCLIToken(r.Context(), c.AccountID, c.KeyID)
	if errors.Is(err, auth.ErrKeyNotFound) {
		// Revoked by a request that raced this one: the outcome is the same.
		err = nil
	}
	if s.authError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetWhoami is GET /v1/whoami.
func (s *Server) GetWhoami(w http.ResponseWriter, r *http.Request) {
	c := s.requireCaller(w, r)
	if c == nil {
		return
	}
	a, u, err := s.Auth.Identity(r.Context(), c.AccountID, c.UserID)
	if s.accountError(w, r, err) {
		return
	}
	out := apigen.Whoami{
		Account:    accountSummaryJSON(*a),
		AuthMethod: apigen.WhoamiAuthMethod(c.Kind),
		Scope:      apigen.ApiKeyScope(c.Scope),
	}
	if u != nil {
		v := userJSON(*u)
		out.User = &v
	}
	if c.KeyID != "" {
		k := c.KeyID
		out.KeyId = &k
	}
	writeJSON(w, http.StatusOK, out)
}

// webURL is where the web app is: WebURL when set, else the request's own
// origin (the API serves the web app, MG-41).
func (s *Server) webURL(r *http.Request) string {
	if s.WebURL != "" {
		return strings.TrimRight(s.WebURL, "/")
	}
	scheme := "https"
	if r.TLS == nil && (strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1")) {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
