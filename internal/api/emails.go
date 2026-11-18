package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// emailsCaller is requireCaller for the email endpoints, which also need
// the emails service.
func (s *Server) emailsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Emails == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Emails need a database, and none is configured."})
		return nil
	}
	return c
}

// emailError writes the response for an emails error, and reports whether
// there was one.
func (s *Server) emailError(w http.ResponseWriter, r *http.Request, err error) bool {
	var docErr *emails.DocError
	var verErr *emails.VersionError
	var posErr *emails.PositionError
	switch {
	case err == nil:
		return false
	case errors.As(err, &posErr):
		WriteError(w, Unprocessable("invalid_position", "That place can't be used: "+posErr.Message+".").
			WithHint("Give after on its own, or index, or parent_id and column (0 left, 1 right) with an optional index."))
	case errors.Is(err, emails.ErrFooterLocked):
		WriteError(w, Unprocessable("footer_locked", "The footer stays the last block: it can't be removed, moved or added, and nothing goes after it."))
	case errors.Is(err, emails.ErrNotBlocks):
		WriteError(w, Unprocessable("kind_mismatch", "This is an html email, which has no blocks.").
			WithHint("Change its html with `mailgoose email replace`."))
	case errors.Is(err, emails.ErrBlockNotFound):
		WriteError(w, NotFound("block_not_found", "This email has no block with that id.").
			WithHint("`mailgoose email blocks list <email>` shows each block's id."))
	case errors.As(err, &docErr):
		WriteError(w, Unprocessable("invalid_document", "The document isn't valid: "+docErr.Error()+".").
			WithHint("The BlockDocument and HtmlDocument schemas in the API reference list every block type and field.").
			WithDetails(map[string]any{"path": docErr.Path}))
	case errors.As(err, &verErr):
		WriteError(w, Conflict("version_conflict", "The email was saved since that version was read, so nothing changed.").
			WithHint("Get the email again and redo the change on the current version.").
			WithDetails(map[string]any{"current_version": verErr.Current}))
	case errors.Is(err, emails.ErrInvalidName):
		WriteError(w, Unprocessable("invalid_name", "An email's name must be 1 to 200 characters on one line."))
	case errors.Is(err, emails.ErrInvalidKind):
		WriteError(w, Unprocessable("kind_mismatch", "That document is for the other kind of email.").
			WithHint("A blocks email takes a BlockDocument and an html email an HtmlDocument; the kind can't change."))
	case errors.Is(err, emails.ErrNotFound):
		WriteError(w, NotFound("not_found", "No email with that id in this account.").
			WithHint("`mailgoose email list` shows the account's emails."))
	default:
		s.Log.ErrorContext(r.Context(), "email request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The database didn't answer.", Hint: "Try again in a minute."})
	}
	return true
}

// emailJSON is the API's Email. The document marshals itself.
type emailJSON struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Version   int              `json:"version"`
	Document  *emails.Document `json:"document"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func toEmailJSON(e *emails.Email) emailJSON {
	return emailJSON{ID: e.ID, Name: e.Name, Kind: e.Kind, Version: e.Version, Document: e.Document,
		CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC()}
}

type emailSummaryJSON struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Version    int       `json:"version"`
	Subject    string    `json:"subject"`
	BlockCount int       `json:"block_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func validEmailID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "email_id must be an email's id."))
		return false
	}
	return true
}

func emailCursor(e *emails.Email) string {
	return e.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + e.ID
}

// ListEmails is GET /v1/emails.
func (s *Server) ListEmails(w http.ResponseWriter, r *http.Request, _ apigen.ListEmailsParams) {
	c := s.emailsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *emails.Cursor
	if pr.After != "" {
		cur, ok := parseContactCursor(pr.After)
		if !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &emails.Cursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	es, err := s.Emails.List(r.Context(), c.AccountID, after, pr.Limit+1)
	if s.emailError(w, r, err) {
		return
	}
	page := NewPage(es, pr.Limit, emailCursor)
	out := struct {
		Data       []emailSummaryJSON `json:"data"`
		NextCursor *string            `json:"next_cursor"`
	}{Data: make([]emailSummaryJSON, len(page.Data)), NextCursor: page.NextCursor}
	for i, e := range page.Data {
		out.Data[i] = emailSummaryJSON{ID: e.ID, Name: e.Name, Kind: e.Kind, Version: e.Version,
			Subject: e.Document.Settings.Subject, BlockCount: len(e.Document.Blocks),
			CreatedAt: e.CreatedAt.UTC(), UpdatedAt: e.UpdatedAt.UTC()}
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateEmail is POST /v1/emails.
func (s *Server) CreateEmail(w http.ResponseWriter, r *http.Request) {
	c := s.emailsCaller(w, r)
	if c == nil {
		return
	}
	var req struct {
		Name     string          `json:"name"`
		Kind     *string         `json:"kind"`
		Document json.RawMessage `json:"document"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	kind := emails.KindBlocks
	if req.Kind != nil {
		kind = *req.Kind
	}
	if kind != emails.KindBlocks && kind != emails.KindHTML {
		WriteError(w, Unprocessable("invalid_kind", "kind must be blocks or html."))
		return
	}
	var doc *emails.Document
	if len(req.Document) > 0 && string(req.Document) != "null" {
		var err error
		if doc, err = emails.ParseDocument(kind, req.Document); s.emailError(w, r, err) {
			return
		}
	}
	e, err := s.Emails.Create(r.Context(), c.AccountID, req.Name, kind, doc)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, toEmailJSON(e))
}

// GetEmail is GET /v1/emails/{email_id}.
func (s *Server) GetEmail(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	e, err := s.Emails.Get(r.Context(), c.AccountID, id)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEmailJSON(e))
}

// UpdateEmail is PATCH /v1/emails/{email_id}.
func (s *Server) UpdateEmail(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	var req struct {
		Version  *int            `json:"version"`
		Name     *string         `json:"name"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if !needVersion(w, req.Version) {
		return
	}
	change := emails.Change{Name: req.Name}
	if len(req.Settings) > 0 && string(req.Settings) != "null" {
		change.Settings = req.Settings
	}
	if change.Name == nil && change.Settings == nil {
		WriteError(w, BadRequest("empty_update", "The request changes nothing.").
			WithHint("Give a name, settings, or both."))
		return
	}
	e, err := s.Emails.Update(r.Context(), c.AccountID, id, *req.Version, change)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEmailJSON(e))
}

// ReplaceEmailDocument is PUT /v1/emails/{email_id}/document.
func (s *Server) ReplaceEmailDocument(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	var req struct {
		Version  *int            `json:"version"`
		Document json.RawMessage `json:"document"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if !needVersion(w, req.Version) {
		return
	}
	if len(req.Document) == 0 || string(req.Document) == "null" {
		WriteError(w, BadRequest("invalid_parameter", "document is required.").
			WithHint("Send the whole document, as getEmail returns it under `document`."))
		return
	}
	e, err := s.Emails.Get(r.Context(), c.AccountID, id)
	if s.emailError(w, r, err) {
		return
	}
	doc, err := emails.ParseDocument(e.Kind, req.Document)
	if s.emailError(w, r, err) {
		return
	}
	e, err = s.Emails.Replace(r.Context(), c.AccountID, id, *req.Version, doc)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEmailJSON(e))
}

func needVersion(w http.ResponseWriter, v *int) bool {
	if v == nil || *v < 1 {
		WriteError(w, BadRequest("invalid_parameter", "version is required: the email's version when you read it.").
			WithHint("Get the email first; its `version` goes here."))
		return false
	}
	return true
}

// DuplicateEmail is POST /v1/emails/{email_id}/duplicate.
func (s *Server) DuplicateEmail(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	if r.ContentLength != 0 {
		if err := DecodeJSON(w, r, &req); err != nil {
			WriteError(w, err)
			return
		}
	}
	name := ""
	if req.Name != nil {
		if *req.Name == "" {
			WriteError(w, Unprocessable("invalid_name", "An email's name must be 1 to 200 characters on one line."))
			return
		}
		name = *req.Name
	}
	e, err := s.Emails.Duplicate(r.Context(), c.AccountID, id, name)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, toEmailJSON(e))
}

// DeleteEmail is DELETE /v1/emails/{email_id}.
func (s *Server) DeleteEmail(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	if s.emailError(w, r, s.Emails.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
