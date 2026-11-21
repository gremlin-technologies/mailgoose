package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/senders"
)

// Sender addresses (MG-85): the addresses an account may send as from the
// shared sending domain.

// senderAddressesCaller is requireCaller (requireFull when full: adding an
// address or resending its link emails someone, which a draft-scope key
// can't do), then a 503 when the server keeps no sender addresses (no
// database, or no system sending domain to email their links from).
func (s *Server) senderAddressesCaller(w http.ResponseWriter, r *http.Request, full bool) *Caller {
	var c *Caller
	if full {
		c = s.requireFull(w, r)
	} else {
		c = s.requireCaller(w, r)
	}
	if c == nil {
		return nil
	}
	if s.SenderAddresses == nil {
		WriteError(w, senderAddressesUnavailable)
		return nil
	}
	return c
}

var senderAddressesUnavailable = &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
	Message: "Sender addresses need a database and a mail sender, and this server has none configured."}

func senderAddressJSON(a *senders.Address) apigen.SenderAddress {
	return apigen.SenderAddress{Id: a.ID, Email: a.Email, Status: apigen.SenderAddressStatus(a.Status),
		ConfirmSentAt: utcPtr(a.ConfirmSentAt), ConfirmExpiresAt: utcPtr(a.ConfirmExpiresAt),
		ConfirmedAt: utcPtr(a.ConfirmedAt), CreatedAt: a.CreatedAt.UTC()}
}

// ListSenderAddresses is GET /v1/sender-addresses.
func (s *Server) ListSenderAddresses(w http.ResponseWriter, r *http.Request) {
	c := s.senderAddressesCaller(w, r, false)
	if c == nil {
		return
	}
	list, err := s.SenderAddresses.List(r.Context(), c.AccountID)
	if s.senderAddressError(w, r, err) {
		return
	}
	out := apigen.SenderAddressList{Data: make([]apigen.SenderAddress, len(list))}
	for i := range list {
		out.Data[i] = senderAddressJSON(&list[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// AddSenderAddress is POST /v1/sender-addresses.
func (s *Server) AddSenderAddress(w http.ResponseWriter, r *http.Request) {
	c := s.senderAddressesCaller(w, r, true)
	if c == nil {
		return
	}
	var req apigen.AddSenderAddressRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	a, err := s.SenderAddresses.Add(r.Context(), c.AccountID, req.Email, s.webURL(r))
	if s.senderAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, senderAddressJSON(a))
}

// ResendSenderAddress is POST /v1/sender-addresses/{sender_address_id}/resend.
func (s *Server) ResendSenderAddress(w http.ResponseWriter, r *http.Request, id apigen.SenderAddressId) {
	c := s.senderAddressesCaller(w, r, true)
	if c == nil || !validSenderAddressID(w, id) {
		return
	}
	a, err := s.SenderAddresses.Resend(r.Context(), c.AccountID, id, s.webURL(r))
	if s.senderAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, senderAddressJSON(a))
}

// RemoveSenderAddress is DELETE /v1/sender-addresses/{sender_address_id}.
func (s *Server) RemoveSenderAddress(w http.ResponseWriter, r *http.Request, id apigen.SenderAddressId) {
	c := s.senderAddressesCaller(w, r, false)
	if c == nil || !validSenderAddressID(w, id) {
		return
	}
	if s.senderAddressError(w, r, s.SenderAddresses.Remove(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ConfirmSenderAddress is POST /v1/sender-addresses/confirm. It needs no
// credentials: the token from the emailed link is the proof.
func (s *Server) ConfirmSenderAddress(w http.ResponseWriter, r *http.Request) {
	if s.SenderAddresses == nil {
		WriteError(w, senderAddressesUnavailable)
		return
	}
	var req apigen.ConfirmSenderAddressRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	got, err := s.SenderAddresses.Confirm(r.Context(), req.Token)
	if s.senderAddressError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.ConfirmedSenderAddress{Email: got.Email, AccountName: got.AccountName,
		ConfirmedAt: got.ConfirmedAt.UTC()})
}

func validSenderAddressID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "sender_address_id must be a sender address's id.").
			WithHint("`mailgoose sender-addresses list` shows them."))
		return false
	}
	return true
}

func (s *Server) senderAddressError(w http.ResponseWriter, r *http.Request, err error) bool {
	var invalid *senders.InvalidError
	var limit *senders.LimitError
	var soon *senders.TooSoonError
	var capped *senders.RateLimitError
	var send *senders.SendError
	switch {
	case err == nil:
		return false
	case errors.Is(err, senders.ErrNotFound):
		WriteError(w, NotFound("not_found", "No sender address with that id in this account.").
			WithHint("`mailgoose sender-addresses list` shows them."))
	case errors.As(err, &invalid):
		WriteError(w, Unprocessable("invalid_email", invalid.Message))
	case errors.Is(err, senders.ErrOwnerAddress):
		WriteError(w, Unprocessable("sender_address_is_owner", "That's the owner's own sign-in address, which you proved at sign-in, so it can already send and needs no slot."))
	case errors.Is(err, senders.ErrExists):
		WriteError(w, Conflict("sender_address_exists", "The account has that sender address already.").
			WithHint("`mailgoose sender-addresses resend <id>` sends its link again if it isn't confirmed yet."))
	case errors.As(err, &limit):
		WriteError(w, Conflict("sender_address_limit", fmt.Sprintf("The account has %d sender addresses, the most it can have.", senders.MaxAddresses)).
			WithHint("Remove one first with `mailgoose sender-addresses remove <id>`."))
	case errors.Is(err, senders.ErrConfirmed):
		WriteError(w, Conflict("sender_address_confirmed", "The sender address is confirmed already, so there's no link to resend."))
	case errors.As(err, &soon):
		secs := max(1, int(math.Ceil(soon.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, TooManyRequests("sender_address_resend_too_soon", "A confirm link went to this address less than a minute ago.").
			WithHint("Wait "+wait(secs)+" (see Retry-After), and check the address's spam folder meanwhile."))
	case errors.As(err, &capped):
		secs := max(1, int(math.Ceil(capped.RetryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, TooManyRequests("sender_address_rate_limited",
			fmt.Sprintf("The account has sent %d sender address confirm emails in the last 24 hours, the most it can. Removing an address doesn't give any back.", senders.DailyEmails)).
			WithHint("Wait "+wait(secs)+" (see Retry-After)."))
	case errors.As(err, &send):
		s.Log.ErrorContext(r.Context(), "send a sender address's confirm link", "err", send.Err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "sender_address_email_failed",
			Message: "The confirm email couldn't be sent; the mail provider refused it or didn't answer. The address is kept, pending.",
			Hint:    "Resend its link in a minute with `mailgoose sender-addresses resend <id>`."})
	case errors.Is(err, senders.ErrBadToken):
		WriteError(w, BadRequest("invalid_token", "That confirm link isn't one. It may have been cut short or changed on the way.").
			WithHint("Open the link straight from the email."))
	case errors.Is(err, senders.ErrTokenNotFound):
		WriteError(w, NotFound("sender_address_link_not_found", "No sender address waits for that link. It may have been removed, or a newer link replaced this one.").
			WithHint("Ask the account's owner to resend the link."))
	case errors.Is(err, senders.ErrExpired):
		WriteError(w, &Error{Status: http.StatusGone, Code: "sender_address_link_expired",
			Message: "That confirm link is older than 7 days, so it no longer works.",
			Hint:    "Ask the account's owner to resend it, from the web app or with `mailgoose sender-addresses resend`."})
	default:
		s.Log.ErrorContext(r.Context(), "sender addresses", "err", err)
		WriteError(w, Internal())
	}
	return true
}
