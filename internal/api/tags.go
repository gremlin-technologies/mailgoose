package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// tagError writes the response for a tag error, and reports whether there
// was one. Anything it doesn't know goes to contactError.
func (s *Server) tagError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, contacts.ErrTagExists):
		WriteError(w, Conflict("tag_exists", "This account already has a tag with that name.").
			WithHint("Tag names are unique ignoring case. `mailgoose tags list` shows them."))
	case errors.Is(err, contacts.ErrTagNotFound):
		WriteError(w, NotFound("not_found", "No tag with that id in this account.").
			WithHint("`mailgoose tags list` shows the account's tags."))
	default:
		return s.contactError(w, r, err)
	}
	return true
}

func tagJSON(t *contacts.Tag) apigen.Tag {
	return apigen.Tag{Id: t.ID, Name: t.Name, ContactCount: t.Contacts, CreatedAt: t.CreatedAt.UTC()}
}

func tagRefsJSON(refs []contacts.TagRef) []apigen.TagRef {
	out := make([]apigen.TagRef, len(refs))
	for i, t := range refs {
		out[i] = apigen.TagRef{Id: t.ID, Name: t.Name}
	}
	return out
}

func validTagID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "tag_id must be a tag's id."))
		return false
	}
	return true
}

// ListTags is GET /v1/tags.
func (s *Server) ListTags(w http.ResponseWriter, r *http.Request, params apigen.ListTagsParams) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *contacts.TagCursor
	if pr.After != "" {
		name, id, ok := strings.Cut(pr.After, "\x00")
		if !ok || !uuidPattern.MatchString(id) {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &contacts.TagCursor{Name: name, ID: id}
	}
	name := ""
	if params.Name != nil {
		name = *params.Name
	}
	ts, err := s.Contacts.ListTags(r.Context(), c.AccountID, name, after, pr.Limit+1)
	if s.tagError(w, r, err) {
		return
	}
	page := NewPage(ts, pr.Limit, func(t *contacts.Tag) string { return t.Name + "\x00" + t.ID })
	out := apigen.TagList{Data: make([]apigen.Tag, len(page.Data)), NextCursor: page.NextCursor}
	for i, t := range page.Data {
		out.Data[i] = tagJSON(t)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateTag is POST /v1/tags.
func (s *Server) CreateTag(w http.ResponseWriter, r *http.Request) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateTagRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	t, err := s.Contacts.CreateTag(r.Context(), c.AccountID, req.Name)
	if s.tagError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, tagJSON(t))
}

// RenameTag is PATCH /v1/tags/{tag_id}.
func (s *Server) RenameTag(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validTagID(w, id) {
		return
	}
	var req apigen.RenameTagRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	t, err := s.Contacts.RenameTag(r.Context(), c.AccountID, id, req.Name)
	if s.tagError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, tagJSON(t))
}

// DeleteTag is DELETE /v1/tags/{tag_id}.
func (s *Server) DeleteTag(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validTagID(w, id) {
		return
	}
	d, err := s.Contacts.DeleteTag(r.Context(), c.AccountID, id)
	if s.tagError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.TagDeleted{ContactsUntagged: d.ContactsUntagged, CampaignsUpdated: d.CampaignsUpdated})
}

// selection reads a ContactSelection body; it writes the error and returns
// false when the body is wrong.
func selection(w http.ResponseWriter, r *http.Request) (contacts.Selection, bool) {
	var req apigen.ContactSelection
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return contacts.Selection{}, false
	}
	var sel contacts.Selection
	if req.ContactIds != nil {
		sel.IDs = *req.ContactIds
		if sel.IDs == nil {
			sel.IDs = []string{}
		}
	}
	if f := req.Filter; f != nil {
		sel.Filter = &contacts.Filter{}
		if f.Email != nil {
			sel.Filter.Email = *f.Email
		}
		if f.Name != nil {
			sel.Filter.Name = *f.Name
		}
		if f.Status != nil {
			if !f.Status.Valid() {
				WriteError(w, Unprocessable("invalid_selection", "filter.status must be unconfirmed, subscribed, unsubscribed, bounced or complained."))
				return sel, false
			}
			sel.Filter.Status = string(*f.Status)
		}
		if f.TagId != nil {
			if !uuidPattern.MatchString(*f.TagId) {
				WriteError(w, Unprocessable("invalid_selection", "filter.tag_id must be a tag's id."))
				return sel, false
			}
			sel.Filter.TagID = *f.TagId
		}
	}
	return sel, true
}

func (s *Server) changeTags(w http.ResponseWriter, r *http.Request, id string, add bool) {
	c := s.contactsCaller(w, r)
	if c == nil || !validTagID(w, id) {
		return
	}
	sel, ok := selection(w, r)
	if !ok {
		return
	}
	var ch *contacts.TagChange
	var err error
	if add {
		ch, err = s.Contacts.TagContacts(r.Context(), c.AccountID, id, sel)
	} else {
		ch, err = s.Contacts.UntagContacts(r.Context(), c.AccountID, id, sel)
	}
	if s.tagError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.TagChange{Matched: ch.Matched, Changed: ch.Changed})
}

// TagContacts is POST /v1/tags/{tag_id}/contacts.
func (s *Server) TagContacts(w http.ResponseWriter, r *http.Request, id string) {
	s.changeTags(w, r, id, true)
}

// UntagContacts is POST /v1/tags/{tag_id}/contacts/remove.
func (s *Server) UntagContacts(w http.ResponseWriter, r *http.Request, id string) {
	s.changeTags(w, r, id, false)
}
