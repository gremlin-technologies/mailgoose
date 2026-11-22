package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
)

// formsCaller is requireCaller for the form endpoints, which also need the
// forms service.
func (s *Server) formsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Forms == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Signup forms need a database, and none is configured."})
		return nil
	}
	return c
}

// formError writes the response for a forms error, and reports whether
// there was one.
func (s *Server) formError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv *forms.InvalidError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inv):
		WriteError(w, Unprocessable("invalid_form", "The form can't be saved: "+inv.Message+".").
			WithDetails(map[string]any{"field": inv.Field}))
	case errors.Is(err, forms.ErrUnknownTag):
		WriteError(w, Unprocessable("unknown_tag", "A tag id isn't one of this account's tags.").
			WithHint("`mailgoose tags list` shows the account's tags."))
	case errors.Is(err, forms.ErrNotFound):
		WriteError(w, NotFound("not_found", "No form with that id in this account.").
			WithHint("`mailgoose form list` shows the account's forms."))
	default:
		s.Log.ErrorContext(r.Context(), "form request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The database didn't answer.", Hint: "Try again in a minute."})
	}
	return true
}

func validFormID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "form_id must be a form's id."))
		return false
	}
	return true
}

func formJSON(f *forms.Form) apigen.Form {
	out := apigen.Form{Id: f.ID, Name: f.Name, ButtonText: f.ButtonText, SuccessMessage: f.SuccessMessage,
		RedirectUrl: f.RedirectURL, DoubleOptIn: f.DoubleOptIn, ConfirmSubject: f.ConfirmSubject,
		ConfirmText: f.ConfirmText, Signups: f.Signups, Confirmations: f.Confirmations, Draft: f.Draft,
		CreatedAt: f.CreatedAt.UTC(), UpdatedAt: f.UpdatedAt.UTC(),
		Fields: make([]apigen.FormField, len(f.Fields)), Tags: make([]apigen.TagRef, len(f.Tags))}
	for i, fl := range f.Fields {
		label, req := fl.Label, fl.Required
		out.Fields[i] = apigen.FormField{Name: apigen.FormFieldName(fl.Name), Label: &label, Required: &req}
	}
	for i, t := range f.Tags {
		out.Tags[i] = apigen.TagRef{Id: t.ID, Name: t.Name}
	}
	return out
}

// formFields turns the API's fields into the service's. Email is required
// unless the request says otherwise, which the service then refuses.
func formFields(in *[]apigen.FormField) *[]forms.Field {
	if in == nil {
		return nil
	}
	out := make([]forms.Field, len(*in))
	for i, fl := range *in {
		f := forms.Field{Name: string(fl.Name), Required: fl.Name == forms.FieldEmail}
		if fl.Label != nil {
			f.Label = *fl.Label
		}
		if fl.Required != nil {
			f.Required = *fl.Required
		}
		out[i] = f
	}
	return &out
}

func formCursor(f *forms.Form) string {
	return f.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + f.ID
}

// ListForms is GET /v1/forms.
func (s *Server) ListForms(w http.ResponseWriter, r *http.Request, _ apigen.ListFormsParams) {
	c := s.formsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *forms.Cursor
	if pr.After != "" {
		cur, ok := parseContactCursor(pr.After)
		if !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &forms.Cursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	fs, err := s.Forms.List(r.Context(), c.AccountID, after, pr.Limit+1)
	if s.formError(w, r, err) {
		return
	}
	page := NewPage(fs, pr.Limit, formCursor)
	out := apigen.FormList{Data: make([]apigen.Form, len(page.Data)), NextCursor: page.NextCursor}
	for i, f := range page.Data {
		out.Data[i] = formJSON(f)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateForm is POST /v1/forms.
func (s *Server) CreateForm(w http.ResponseWriter, r *http.Request) {
	c := s.formsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateFormRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	f, err := s.Forms.Create(r.Context(), c.AccountID, forms.Changes{
		Name: &req.Name, Fields: formFields(req.Fields), TagIDs: req.TagIds, ButtonText: req.ButtonText,
		SuccessMessage: req.SuccessMessage, RedirectURL: req.RedirectUrl, DoubleOptIn: req.DoubleOptIn,
		ConfirmSubject: req.ConfirmSubject, ConfirmText: req.ConfirmText,
	})
	if s.formError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, formJSON(f))
}

// GetForm is GET /v1/forms/{form_id}.
func (s *Server) GetForm(w http.ResponseWriter, r *http.Request, id string) {
	c := s.formsCaller(w, r)
	if c == nil || !validFormID(w, id) {
		return
	}
	f, err := s.Forms.Get(r.Context(), c.AccountID, id)
	if s.formError(w, r, err) {
		return
	}
	out := formJSON(f)
	problems := []apigen.FormProblem{}
	if s.Signups != nil {
		ps, err := s.Signups.Problems(r.Context(), f)
		if err != nil {
			s.Log.ErrorContext(r.Context(), "check a form's problems", "err", err)
			WriteError(w, Internal())
			return
		}
		for _, p := range ps {
			problems = append(problems, apigen.FormProblem{Code: p.Code, Message: p.Message, Hint: p.Hint})
		}
	}
	out.Problems = &problems
	writeJSON(w, http.StatusOK, out)
}

// UpdateForm is PATCH /v1/forms/{form_id}.
func (s *Server) UpdateForm(w http.ResponseWriter, r *http.Request, id string) {
	c := s.formsCaller(w, r)
	if c == nil || !validFormID(w, id) {
		return
	}
	var req apigen.UpdateFormRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	f, err := s.Forms.Update(r.Context(), c.AccountID, id, forms.Changes{
		Name: req.Name, Fields: formFields(req.Fields), TagIDs: req.TagIds, ButtonText: req.ButtonText,
		SuccessMessage: req.SuccessMessage, RedirectURL: req.RedirectUrl, DoubleOptIn: req.DoubleOptIn,
		ConfirmSubject: req.ConfirmSubject, ConfirmText: req.ConfirmText,
	})
	if s.formError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, formJSON(f))
}

// DeleteForm is DELETE /v1/forms/{form_id}.
func (s *Server) DeleteForm(w http.ResponseWriter, r *http.Request, id string) {
	c := s.formsCaller(w, r)
	if c == nil || !validFormID(w, id) {
		return
	}
	if s.formError(w, r, s.Forms.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetFormEmbed is GET /v1/forms/{form_id}/embed.
func (s *Server) GetFormEmbed(w http.ResponseWriter, r *http.Request, id string) {
	c := s.formsCaller(w, r)
	if c == nil || !validFormID(w, id) {
		return
	}
	f, err := s.Forms.Get(r.Context(), c.AccountID, id)
	if s.formError(w, r, err) {
		return
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	e := forms.MakeEmbed(f, s.webURL(r), now())
	writeJSON(w, http.StatusOK, apigen.FormEmbed{ActionUrl: e.ActionURL, Html: e.HTML, Script: e.Script})
}
