package api

import (
	"errors"
	"html"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// previewRecipient is the sample person a preview is filled in for.
var previewRecipient = render.Recipient{
	Email:          "alex@example.com",
	FirstName:      "Alex",
	LastName:       "Rivera",
	UnsubscribeURL: "#unsubscribe",
}

// previewPostalAddress stands in for an account's postal address until it
// has one, so a new account can still see its email.
const previewPostalAddress = "Your postal address goes here.\nAdd it in Settings before you send."

// mobilePreviewWidth is the frame a mobile preview shows the email in: a
// common phone's width, narrow enough for the email's phone rules to apply.
const mobilePreviewWidth = "375"

// previewCSP lets a preview show its images and styles and nothing else:
// no scripts, forms or plugins, and only this API may frame it.
const previewCSP = "default-src 'none'; img-src https: http: data:; style-src 'unsafe-inline'; font-src https: data:; " +
	"frame-ancestors 'self'; sandbox"

// PreviewEmail is GET /v1/emails/{email_id}/preview.
func (s *Server) PreviewEmail(w http.ResponseWriter, r *http.Request, id string, params apigen.PreviewEmailParams) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	width := "desktop"
	if params.Width != nil {
		width = string(*params.Width)
	}
	if width != "desktop" && width != "mobile" {
		WriteError(w, BadRequest("invalid_parameter", "width must be desktop or mobile."))
		return
	}
	e, err := s.Emails.Get(r.Context(), c.AccountID, id)
	if s.emailError(w, r, err) {
		return
	}
	acct := render.Account{PostalAddress: previewPostalAddress, AssetsURL: s.webURL(r)}
	rcpt := previewRecipient
	if s.Contacts != nil {
		// The account's contact fields are merge tags too (MG-133); the
		// sample person has no values for them, so each shows its fallback.
		if acct.FieldKeys, err = s.Contacts.FieldKeys(r.Context(), c.AccountID); err != nil {
			s.emailError(w, r, err)
			return
		}
		if params.ContactId != nil {
			if !uuidPattern.MatchString(*params.ContactId) {
				WriteError(w, BadRequest("invalid_parameter", "contact_id must be a contact's id."))
				return
			}
			ct, err := s.Contacts.Get(r.Context(), c.AccountID, *params.ContactId)
			if errors.Is(err, contacts.ErrNotFound) {
				WriteError(w, NotFound("not_found", "No contact with that id in this account.").
					WithHint("`mailgoose contacts list` shows the account's contacts."))
				return
			}
			if err != nil {
				s.emailError(w, r, err)
				return
			}
			rcpt.Email, rcpt.FirstName, rcpt.LastName, rcpt.Fields = ct.Email, deref(ct.FirstName), deref(ct.LastName), ct.Fields
		}
	}
	branded := false
	if s.Auth != nil {
		st, err := s.Auth.AccountSettings(r.Context(), c.AccountID)
		if err != nil {
			s.emailError(w, r, err)
			return
		}
		if st.PostalAddress != nil && strings.TrimSpace(*st.PostalAddress) != "" {
			acct.PostalAddress = *st.PostalAddress
		}
		branded = st.Plan == "free"
	}

	var tpl *render.Template
	if e.Document.HTML != nil {
		if strings.TrimSpace(*e.Document.HTML) == "" {
			WriteError(w, Unprocessable("empty_email", "This html email has no markup yet, so there's nothing to preview.").
				WithHint("Add it with `mailgoose email import-html <email> <file.html>`."))
			return
		}
		tpl, err = render.CompileHTML(e.Document, acct)
	} else {
		tpl, err = render.Compile(e.Document, acct)
	}
	var htmlErr *emails.HTMLError
	if errors.As(err, &htmlErr) {
		WriteError(w, Unprocessable("invalid_html", "The email's markup has errors: "+htmlErr.Error()+".").
			WithHint("Fix each problem in details.problems and import the markup again with `mailgoose email import-html <email> <file.html>`.").
			WithDetails(map[string]any{"problems": htmlErr.Issues}))
		return
	}
	var problems render.Problems
	if errors.As(err, &problems) {
		list := make([]map[string]string, 0, len(problems))
		for _, p := range problems {
			list = append(list, map[string]string{"path": p.Path, "message": p.Message})
		}
		first := problems[0]
		WriteError(w, Unprocessable("invalid_merge_tags", "The email can't be filled in: "+first.Path+": "+first.Message+".").
			WithHint("Fix each problem in details.problems; `mailgoose email blocks list <email>` shows where each block is.").
			WithDetails(map[string]any{"problems": list}))
		return
	}
	if err != nil {
		s.emailError(w, r, err)
		return
	}
	// The preview shows the "Sent by mailgoose" footer a free account's
	// copies carry (MG-119).
	rcpt.Branded = branded
	if s.Sending != nil {
		if rcpt.Branded, err = sending.Branded(r.Context(), s.Sending.DB, c.AccountID); err != nil {
			s.emailError(w, r, err)
			return
		}
	}
	page := tpl.Personalize(rcpt).HTML
	if width == "mobile" {
		page = mobileFrame(page)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", previewCSP)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

// mobileFrame shows an email in a phone-wide frame, whose own viewport
// makes the email's narrow-screen rules apply.
func mobileFrame(email string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mobile preview</title>
</head>
<body style="margin:0;padding:24px 0;background-color:#e4e4e7;">
<iframe title="Email at ` + mobilePreviewWidth + ` pixels" srcdoc="` + html.EscapeString(email) + `" style="display:block;width:` +
		mobilePreviewWidth + `px;height:calc(100vh - 48px);margin:0 auto;border:0;background-color:#ffffff;"></iframe>
</body>
</html>
`
}
