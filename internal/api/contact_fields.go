package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// fieldError writes the response for a contact field error, and reports
// whether there was one. Anything it doesn't know goes to contactError.
func (s *Server) fieldError(w http.ResponseWriter, r *http.Request, err error) bool {
	var le *contacts.FieldLimitError
	var ke *contacts.FieldKeyExistsError
	switch {
	case errors.Is(err, contacts.ErrFieldExists):
		WriteError(w, Conflict("field_exists", "This account already has a contact field with that name.").
			WithHint("Field names are unique ignoring case. `mailgoose fields list` shows them."))
	case errors.As(err, &ke):
		WriteError(w, Conflict("field_exists", fmt.Sprintf("This account already has a contact field with the key %s, which that name works out to.", ke.Key)).
			WithHint("Pick a name that differs in more than case or punctuation. `mailgoose fields list` shows the keys.").
			WithDetails(map[string]any{"key": ke.Key}))
	case errors.As(err, &le):
		WriteError(w, Conflict("field_limit", fmt.Sprintf("An account can have at most %d contact fields, and this one has %d.", le.Limit, le.Count)).
			WithHint("Delete a field you no longer use to add another.").
			WithDetails(map[string]any{"limit": le.Limit, "count": le.Count}))
	case errors.Is(err, contacts.ErrFieldNotFound):
		WriteError(w, NotFound("not_found", "No contact field with that id in this account.").
			WithHint("`mailgoose fields list` shows the account's fields."))
	default:
		return s.contactError(w, r, err)
	}
	return true
}

func fieldJSON(f *contacts.Field) apigen.ContactField {
	return apigen.ContactField{Id: f.ID, Key: f.Key, Name: f.Name, CreatedAt: f.CreatedAt.UTC(), UpdatedAt: f.UpdatedAt.UTC()}
}

func validFieldID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "field_id must be a contact field's id."))
		return false
	}
	return true
}

// ListContactFields is GET /v1/contact-fields.
func (s *Server) ListContactFields(w http.ResponseWriter, r *http.Request, _ apigen.ListContactFieldsParams) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *contacts.FieldCursor
	if pr.After != "" {
		cur, ok := parseContactCursor(pr.After)
		if !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &contacts.FieldCursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	fs, err := s.Contacts.ListFields(r.Context(), c.AccountID, after, pr.Limit+1)
	if s.fieldError(w, r, err) {
		return
	}
	page := NewPage(fs, pr.Limit, func(f *contacts.Field) string {
		return contactCursor(&contacts.Contact{CreatedAt: f.CreatedAt, ID: f.ID})
	})
	out := apigen.ContactFieldList{Data: make([]apigen.ContactField, len(page.Data)), NextCursor: page.NextCursor}
	for i, f := range page.Data {
		out.Data[i] = fieldJSON(f)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateContactField is POST /v1/contact-fields.
func (s *Server) CreateContactField(w http.ResponseWriter, r *http.Request) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateContactFieldRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	f, err := s.Contacts.CreateField(r.Context(), c.AccountID, req.Name)
	if s.fieldError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, fieldJSON(f))
}

// RenameContactField is PATCH /v1/contact-fields/{field_id}.
func (s *Server) RenameContactField(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validFieldID(w, id) {
		return
	}
	var req apigen.RenameContactFieldRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	f, err := s.Contacts.RenameField(r.Context(), c.AccountID, id, req.Name)
	if s.fieldError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, fieldJSON(f))
}

// DeleteContactField is DELETE /v1/contact-fields/{field_id}.
func (s *Server) DeleteContactField(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validFieldID(w, id) {
		return
	}
	d, err := s.Contacts.DeleteField(r.Context(), c.AccountID, id)
	if s.fieldError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.ContactFieldDeleted{ValuesDeleted: d.ValuesDeleted})
}
