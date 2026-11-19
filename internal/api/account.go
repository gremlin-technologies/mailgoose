package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// GetAccount is GET /v1/account.
func (s *Server) GetAccount(w http.ResponseWriter, r *http.Request) {
	c := s.requireCaller(w, r)
	if c == nil {
		return
	}
	st, err := s.Auth.AccountSettings(r.Context(), c.AccountID)
	if s.accountError(w, r, err) {
		return
	}
	s.writeAccount(w, r, st)
}

// writeAccount writes the account's settings with its send limit.
func (s *Server) writeAccount(w http.ResponseWriter, r *http.Request, st *auth.Settings) {
	var limit sending.SendLimit
	if s.Sending != nil {
		now := time.Now()
		if s.Now != nil {
			now = s.Now()
		}
		var err error
		if limit, err = s.Sending.Limits.Account(r.Context(), s.Sending.DB, st.ID, now); err != nil {
			WriteError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, accountJSON(st, limit))
}

// nullable is a JSON field that tells "absent" (Set false) from null (Set
// true, Value nil), which a plain pointer can't.
type nullable struct {
	Set   bool
	Value *string
}

func (n *nullable) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(b, []byte("null")) {
		n.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	n.Value = &v
	return nil
}

func (n nullable) field() auth.Field { return auth.Field{Set: n.Set, Value: n.Value} }

// updateAccountBody is apigen.UpdateAccountRequest with null kept apart from
// absent.
type updateAccountBody struct {
	Name          nullable `json:"name"`
	FromName      nullable `json:"from_name"`
	FromEmail     nullable `json:"from_email"`
	PostalAddress nullable `json:"postal_address"`
	TimeZone      nullable `json:"time_zone"`
}

// UpdateAccount is PATCH /v1/account.
func (s *Server) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	c := s.requireCaller(w, r)
	if c == nil {
		return
	}
	var req updateAccountBody
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	u := auth.SettingsUpdate{
		Name:          req.Name.field(),
		FromName:      req.FromName.field(),
		FromEmail:     req.FromEmail.field(),
		PostalAddress: req.PostalAddress.field(),
		TimeZone:      req.TimeZone.field(),
	}
	if !u.Name.Set && !u.FromName.Set && !u.FromEmail.Set && !u.PostalAddress.Set && !u.TimeZone.Set {
		WriteError(w, BadRequest("empty_update", "The request changes nothing.").
			WithHint("Send at least one of name, from_name, from_email, postal_address or time_zone."))
		return
	}
	st, err := s.Auth.UpdateAccountSettings(r.Context(), c.AccountID, u)
	if s.accountError(w, r, err) {
		return
	}
	s.writeAccount(w, r, st)
}

func (s *Server) accountError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, auth.ErrNoAccount) {
		// The caller's account was deleted after it authenticated.
		WriteError(w, Unauthorized("unauthenticated", "This account no longer exists."))
		return true
	}
	if errors.Is(err, auth.ErrConfirmMismatch) {
		WriteError(w, Unprocessable("confirmation_mismatch", "The confirmation isn't this account's name, so nothing was deleted.").
			WithHint("Type the account's name exactly as it shows in settings or `mailgoose account get`."))
		return true
	}
	return s.authError(w, r, err)
}

func accountJSON(st *auth.Settings, limit sending.SendLimit) apigen.AccountSettings {
	return apigen.AccountSettings{
		Id:             st.ID,
		Name:           st.Name,
		Plan:           apigen.AccountSettingsPlan(st.Plan),
		FromName:       st.FromName,
		FromEmail:      st.FromEmail,
		PostalAddress:  st.PostalAddress,
		TimeZone:       st.TimeZone,
		MissingForSend: append([]string{}, st.MissingForSend()...),
		SendLimit: apigen.SendLimit{Daily: limit.Daily, Usual: limit.Usual, Reason: limit.Reason,
			LoweredUntil: limit.LoweredUntil},
		Terms:     termsJSON(st),
		CreatedAt: st.CreatedAt.UTC(),
	}
}

// DeleteAccount is DELETE /v1/account (AUTH-6). The confirmation is checked
// before anything is removed. Sending domains go first, through the domains
// service, so their Amazon SES identities are removed too; then the rest of
// the account goes in one transaction.
func (s *Server) DeleteAccount(w http.ResponseWriter, r *http.Request, params apigen.DeleteAccountParams) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	if params.Confirm == nil || *params.Confirm == "" {
		WriteError(w, BadRequest("invalid_parameter", "confirm is required: the account's name, exactly.").
			WithHint("Deleting an account can't be undone. `mailgoose account get` shows its name."))
		return
	}
	st, err := s.Auth.AccountSettings(r.Context(), c.AccountID)
	if s.accountError(w, r, err) {
		return
	}
	if *params.Confirm != st.Name {
		s.accountError(w, r, auth.ErrConfirmMismatch)
		return
	}
	// A deleted account stops paying at once (BIL-4): cancel first, so a
	// failure leaves the account whole and the person can try again.
	if s.Billing != nil {
		if err := s.Billing.CancelSubscription(r.Context(), c.AccountID); s.billingError(w, r, err) {
			return
		}
	}
	if s.Domains != nil {
		for {
			ds, err := s.Domains.List(r.Context(), c.AccountID, "", "", 100)
			if s.domainError(w, r, err) {
				return
			}
			if len(ds) == 0 {
				break
			}
			for _, d := range ds {
				if err := s.Domains.Delete(r.Context(), c.AccountID, d.ID); s.domainError(w, r, err) {
					return
				}
			}
		}
	}
	if s.Images != nil {
		if err := s.Images.DeleteAccount(r.Context(), c.AccountID); s.imageError(w, r, err) {
			return
		}
	}
	if s.accountError(w, r, s.Auth.DeleteAccount(r.Context(), c.AccountID, *params.Confirm)) {
		return
	}
	s.Log.InfoContext(r.Context(), "account deleted", "account_id", c.AccountID)
	if c.Kind == KindSession {
		clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}
