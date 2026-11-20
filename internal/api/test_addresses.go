package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
)

// Test addresses (MG-80): who an account in test mode may send to.

// testAddressesCaller is requireCaller (requireFull when full: adding an
// address or resending its link emails someone, which a draft-scope key
// can't do), then a 503 when the server keeps no test addresses (no
// database, or no system sending domain to email their links from).
func (s *Server) testAddressesCaller(w http.ResponseWriter, r *http.Request, full bool) *Caller {
	var c *Caller
	if full {
		c = s.requireFull(w, r)
	} else {
		c = s.requireCaller(w, r)
	}
	if c == nil {
		return nil
	}
	if s.TestAddresses == nil {
		WriteError(w, testAddressesUnavailable)
		return nil
	}
	return c
}

var testAddressesUnavailable = &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
	Message: "Test addresses need a database and a mail sender, and this server has none configured."}

func testAddressJSON(a *testmode.Address) apigen.TestAddress {
	return apigen.TestAddress{Id: a.ID, Email: a.Email, Status: apigen.TestAddressStatus(a.Status),
		ConfirmSentAt: utcPtr(a.ConfirmSentAt), ConfirmExpiresAt: utcPtr(a.ConfirmExpiresAt),
		ConfirmedAt: utcPtr(a.ConfirmedAt), CreatedAt: a.CreatedAt.UTC()}
}

// ListTestAddresses is GET /v1/test-addresses.
func (s *Server) ListTestAddresses(w http.ResponseWriter, r *http.Request) {
	c := s.testAddressesCaller(w, r, false)
	if c == nil {
		return
	}
	list, err := s.TestAddresses.List(r.Context(), c.AccountID)
	if s.testAddressError(w, r, err) {
		return
	}
	out := apigen.TestAddressList{Data: make([]apigen.TestAddress, len(list))}
	for i := range list {
		out.Data[i] = testAddressJSON(&list[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// AddTestAddress is POST /v1/test-addresses.
func (s *Server) AddTestAddress(w http.ResponseWriter, r *http.Request) {
	c := s.testAddressesCaller(w, r, true)
	if c == nil {
		return
	}
	var req apigen.AddTestAddressRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	a, err := s.TestAddresses.Add(r.Context(), c.AccountID, req.Email, s.webURL(r))
	if s.testAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, testAddressJSON(a))
}

// ResendTestAddress is POST /v1/test-addresses/{test_address_id}/resend.
func (s *Server) ResendTestAddress(w http.ResponseWriter, r *http.Request, id apigen.TestAddressId) {
	c := s.testAddressesCaller(w, r, true)
	if c == nil || !validTestAddressID(w, id) {
		return
	}
	a, err := s.TestAddresses.Resend(r.Context(), c.AccountID, id, s.webURL(r))
	if s.testAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, testAddressJSON(a))
}

// RemoveTestAddress is DELETE /v1/test-addresses/{test_address_id}.
func (s *Server) RemoveTestAddress(w http.ResponseWriter, r *http.Request, id apigen.TestAddressId) {
	c := s.testAddressesCaller(w, r, false)
	if c == nil || !validTestAddressID(w, id) {
		return
	}
	if s.testAddressError(w, r, s.TestAddresses.Remove(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ConfirmTestAddress is POST /v1/test-addresses/confirm. It needs no
// credentials: the token from the emailed link is the proof.
func (s *Server) ConfirmTestAddress(w http.ResponseWriter, r *http.Request) {
	if s.TestAddresses == nil {
		WriteError(w, testAddressesUnavailable)
		return
	}
	var req apigen.ConfirmTestAddressRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	got, err := s.TestAddresses.Confirm(r.Context(), req.Token)
	if s.testAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.ConfirmedTestAddress{Email: got.Email, AccountName: got.AccountName,
		ConfirmedAt: got.ConfirmedAt.UTC()})
}

func validTestAddressID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "test_address_id must be a test address's id.").
			WithHint("`mailgoose test-addresses list` shows them."))
		return false
	}
	return true
}

func (s *Server) testAddressError(w http.ResponseWriter, r *http.Request, err error) bool {
	var invalid *testmode.InvalidError
	var limit *testmode.LimitError
	var soon *testmode.TooSoonError
	var capped *testmode.RateLimitError
	var send *testmode.SendError
	switch {
	case err == nil:
		return false
	case errors.Is(err, testmode.ErrNotFound):
		WriteError(w, NotFound("not_found", "No test address with that id in this account.").
			WithHint("`mailgoose test-addresses list` shows them."))
	case errors.As(err, &invalid):
		WriteError(w, Unprocessable("invalid_email", invalid.Message))
	case errors.Is(err, testmode.ErrOwnerAddress):
		WriteError(w, Unprocessable("test_address_is_owner", "That's the owner's own sign-in address, which test mode always sends to, so it needs no slot."))
	case errors.Is(err, testmode.ErrExists):
		WriteError(w, Conflict("test_address_exists", "The account has that test address already.").
			WithHint("`mailgoose test-addresses resend <id>` sends its link again if it isn't confirmed yet."))
	case errors.As(err, &limit):
		WriteError(w, Conflict("test_address_limit", "The account has 5 test addresses, the most it can have.").
			WithHint("Remove one first with `mailgoose test-addresses remove <id>`."))
	case errors.Is(err, testmode.ErrConfirmed):
		WriteError(w, Conflict("test_address_confirmed", "The test address is confirmed already, so there's no link to resend."))
	case errors.As(err, &soon):
		secs := max(1, int(math.Ceil(soon.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, TooManyRequests("test_address_resend_too_soon", "A confirm link went to this address less than a minute ago.").
			WithHint("Wait "+wait(secs)+" (see Retry-After), and check the address's spam folder meanwhile."))
	case errors.As(err, &capped):
		secs := max(1, int(math.Ceil(capped.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, TooManyRequests("test_address_rate_limited",
			fmt.Sprintf("The account has sent %d confirm emails in the last 24 hours, the most it can. Removing an address doesn't give any back.", testmode.DailyEmails)).
			WithHint("Wait "+wait(secs)+" (see Retry-After)."))
	case errors.As(err, &send):
		s.Log.ErrorContext(r.Context(), "send a test address's confirm link", "err", send.Err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "test_address_email_failed",
			Message: "The confirm email couldn't be sent; the mail provider refused it or didn't answer. The address is kept, pending.",
			Hint:    "Resend its link in a minute with `mailgoose test-addresses resend <id>`."})
	case errors.Is(err, testmode.ErrBadToken):
		WriteError(w, BadRequest("invalid_token", "That confirm link isn't one. It may have been cut short or changed on the way.").
			WithHint("Open the link straight from the email."))
	case errors.Is(err, testmode.ErrTokenNotFound):
		WriteError(w, NotFound("test_address_link_not_found", "No test address waits for that link. It may have been removed, or a newer link replaced this one.").
			WithHint("Ask the account's owner to resend the link."))
	case errors.Is(err, testmode.ErrExpired):
		WriteError(w, &Error{Status: http.StatusGone, Code: "test_address_link_expired",
			Message: "That confirm link is older than 7 days, so it no longer works.",
			Hint:    "Ask the account's owner to resend it, from the web app or with `mailgoose test-addresses resend`."})
	default:
		s.Log.ErrorContext(r.Context(), "test addresses", "err", err)
		WriteError(w, Internal())
	}
	return true
}
