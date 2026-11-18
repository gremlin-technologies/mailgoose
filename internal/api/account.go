package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
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
	writeJSON(w, http.StatusOK, accountJSON(st))
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
	writeJSON(w, http.StatusOK, accountJSON(st))
}

func (s *Server) accountError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, auth.ErrNoAccount) {
		// The caller's account was deleted after it authenticated.
		WriteError(w, Unauthorized("unauthenticated", "This account no longer exists."))
		return true
	}
	return s.authError(w, r, err)
}

func accountJSON(st *auth.Settings) apigen.AccountSettings {
	return apigen.AccountSettings{
		Id:             st.ID,
		Name:           st.Name,
		Plan:           apigen.AccountSettingsPlan(st.Plan),
		FromName:       st.FromName,
		FromEmail:      st.FromEmail,
		PostalAddress:  st.PostalAddress,
		TimeZone:       st.TimeZone,
		MissingForSend: append([]string{}, st.MissingForSend()...),
		CreatedAt:      st.CreatedAt.UTC(),
	}
}
