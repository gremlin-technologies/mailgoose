package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// contactsCaller is requireCaller for the contact endpoints, which also need
// the contacts service.
func (s *Server) contactsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Contacts == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Contacts need a database, and none is configured."})
		return nil
	}
	return c
}

// contactError writes the response for a contacts error, and reports
// whether there was one.
func (s *Server) contactError(w http.ResponseWriter, r *http.Request, err error) bool {
	var ve *auth.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &ve):
		WriteError(w, Unprocessable(ve.Code, ve.Message))
	case errors.Is(err, contacts.ErrExists):
		WriteError(w, Conflict("contact_exists", "This account already has a contact with that address.").
			WithHint("`mailgoose contacts get <email>` shows it."))
	case errors.Is(err, contacts.ErrSuppressed):
		WriteError(w, Unprocessable("contact_suppressed", "This contact unsubscribed, bounced or complained, so it can't be subscribed again here.").
			WithHint("The person can subscribe again through a signup form."))
	case errors.Is(err, contacts.ErrNotFound):
		WriteError(w, NotFound("not_found", "No contact with that id in this account.").
			WithHint("`mailgoose contacts list --email <address>` finds a contact by address."))
	default:
		s.Log.ErrorContext(r.Context(), "contact request failed", "err", err)
		WriteError(w, Internal())
	}
	return true
}

func contactJSON(c *contacts.Contact) apigen.Contact {
	return apigen.Contact{
		Id:              c.ID,
		Email:           c.Email,
		FirstName:       c.FirstName,
		LastName:        c.LastName,
		Status:          apigen.ContactStatus(c.Status),
		Source:          apigen.ContactSource(c.Source),
		Tags:            tagRefsJSON(c.Tags),
		CreatedAt:       c.CreatedAt.UTC(),
		UpdatedAt:       c.UpdatedAt.UTC(),
		StatusChangedAt: c.StatusChangedAt.UTC(),
	}
}

// contactCursor joins a contact's keyset values into a cursor value.
func contactCursor(c *contacts.Contact) string {
	return c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
}

func parseContactCursor(v string) (*contacts.Cursor, bool) {
	ts, id, ok := strings.Cut(v, "|")
	if !ok || !uuidPattern.MatchString(id) {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, false
	}
	return &contacts.Cursor{CreatedAt: t, ID: id}, true
}

// ListContacts is GET /v1/contacts.
func (s *Server) ListContacts(w http.ResponseWriter, r *http.Request, params apigen.ListContactsParams) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *contacts.Cursor
	if pr.After != "" {
		var ok bool
		if after, ok = parseContactCursor(pr.After); !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	var f contacts.Filter
	if params.Email != nil {
		f.Email = *params.Email
	}
	if params.Name != nil {
		f.Name = *params.Name
	}
	if params.Status != nil {
		if !params.Status.Valid() {
			WriteError(w, BadRequest("invalid_parameter", "status must be unconfirmed, subscribed, unsubscribed, bounced or complained."))
			return
		}
		f.Status = string(*params.Status)
	}
	if params.TagId != nil {
		if !uuidPattern.MatchString(*params.TagId) {
			WriteError(w, BadRequest("invalid_parameter", "tag_id must be a tag's id."))
			return
		}
		f.TagID = *params.TagId
	}
	cs, err := s.Contacts.List(r.Context(), c.AccountID, f, after, pr.Limit+1)
	if s.contactError(w, r, err) {
		return
	}
	page := NewPage(cs, pr.Limit, contactCursor)
	out := apigen.ContactList{Data: make([]apigen.Contact, len(page.Data)), NextCursor: page.NextCursor}
	for i, ct := range page.Data {
		out.Data[i] = contactJSON(ct)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateContact is POST /v1/contacts.
func (s *Server) CreateContact(w http.ResponseWriter, r *http.Request) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateContactRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	in := contacts.CreateInput{Email: req.Email, FirstName: req.FirstName, LastName: req.LastName}
	if req.Status != nil {
		in.Status = string(*req.Status)
	}
	if req.Source != nil {
		// Forms and imports set their own sources.
		if *req.Source != apigen.CreateContactRequestSource(contacts.SourceAPI) && *req.Source != apigen.CreateContactRequestSource(contacts.SourceManual) {
			WriteError(w, Unprocessable("invalid_source", "source must be api or manual."))
			return
		}
		in.Source = string(*req.Source)
	}
	ct, err := s.Contacts.Create(r.Context(), c.AccountID, in)
	if s.contactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, contactJSON(ct))
}

func validContactID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "contact_id must be a contact's id."))
		return false
	}
	return true
}

// GetContact is GET /v1/contacts/{contact_id}.
func (s *Server) GetContact(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validContactID(w, id) {
		return
	}
	ct, err := s.Contacts.Get(r.Context(), c.AccountID, id)
	if s.contactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, contactJSON(ct))
}

// updateContactBody is apigen.UpdateContactRequest with null kept apart
// from absent.
type updateContactBody struct {
	Email     nullable `json:"email"`
	FirstName nullable `json:"first_name"`
	LastName  nullable `json:"last_name"`
	Status    nullable `json:"status"`
}

// UpdateContact is PATCH /v1/contacts/{contact_id}.
func (s *Server) UpdateContact(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validContactID(w, id) {
		return
	}
	var req updateContactBody
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	u := contacts.UpdateInput{Email: req.Email.field(), FirstName: req.FirstName.field(), LastName: req.LastName.field(), Status: req.Status.field()}
	if !u.Email.Set && !u.FirstName.Set && !u.LastName.Set && !u.Status.Set {
		WriteError(w, BadRequest("empty_update", "The request changes nothing.").
			WithHint("Send at least one of email, first_name, last_name or status."))
		return
	}
	ct, err := s.Contacts.Update(r.Context(), c.AccountID, id, u)
	if s.contactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, contactJSON(ct))
}

// DeleteContact is DELETE /v1/contacts/{contact_id}.
func (s *Server) DeleteContact(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validContactID(w, id) {
		return
	}
	if s.contactError(w, r, s.Contacts.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
