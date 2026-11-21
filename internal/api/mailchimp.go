package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Importing a Mailchimp export (MG-72): upload, preview, confirm.

const mailchimpUploadPath = "/v1/imports/mailchimp/bundles"

// MaxMailchimpBodyBytes is the largest upload request: the largest export
// with room for the form's other parts.
const MaxMailchimpBodyBytes = mailchimp.MaxUploadBytes + 1<<20

// mailchimpUploadTime is how long an upload may take to arrive.
const mailchimpUploadTime = 10 * time.Minute

// mailchimpUploads limits how many exports one API task reads at once, since
// each is held in memory while it's read.
var mailchimpUploads = make(chan struct{}, 2)

// mailchimpCaller is the caller of a Mailchimp import endpoint, or nil after
// writing the error.
func (s *Server) mailchimpCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && (s.Contacts == nil || s.Mailchimp == nil || s.Emails == nil) {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Importing from Mailchimp needs a database, and none is configured."})
		return nil
	}
	return c
}

// mailchimpError writes the response for a Mailchimp import error, and
// reports whether there was one.
func (s *Server) mailchimpError(w http.ResponseWriter, r *http.Request, err error) bool {
	var re *mailchimp.Error
	var ve *auth.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &re):
		WriteError(w, Unprocessable(re.Code, re.Message))
	case errors.As(err, &ve):
		e := Unprocessable(ve.Code, ve.Message)
		if ve.Code == "permission_required" {
			e = e.WithHint("Send `permission_confirmed: true`, or `--permission-confirmed` in the CLI, once you're sure.")
		}
		WriteError(w, e)
	case errors.Is(err, mailchimp.ErrUploadNotFound):
		WriteError(w, NotFound("not_found", "No uploaded Mailchimp export with that id in this account; uploads are kept for 7 days.").
			WithHint("`mailgoose import mailchimp upload <file>` uploads it again."))
	case errors.Is(err, mailchimp.ErrUploadUsed):
		WriteError(w, Unprocessable("upload_used", "This export was already imported, and its file removed.").
			WithHint("Upload it again to import it again."))
	default:
		s.Log.ErrorContext(r.Context(), "mailchimp import request failed", "err", err)
		WriteError(w, Internal())
	}
	return true
}

// readMailchimpUpload reads the upload form: the file, and how to read it.
func readMailchimpUpload(w http.ResponseWriter, r *http.Request) (name string, data []byte, opts mailchimp.Options, err error) {
	mt, params, perr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if perr != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return "", nil, opts, BadRequest("unsupported_content_type", "Send the export as multipart/form-data, in a part named file.").
			WithHint("With curl: -F file=@mailchimp-export.zip")
	}
	mr := multipart.NewReader(http.MaxBytesReader(w, r.Body, MaxMailchimpBodyBytes), params["boundary"])
	tooBig := func(e error) error {
		var mbe *http.MaxBytesError
		if errors.As(e, &mbe) {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large",
				Message: fmt.Sprintf("The export is over %d MB.", mailchimp.MaxUploadBytes>>20)}
		}
		return nil
	}
	var audience string
	var groups []string
	seenFile := false
	for {
		p, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			if e := tooBig(perr); e != nil {
				return "", nil, opts, e
			}
			return "", nil, opts, BadRequest("invalid_multipart", "The form can't be read: "+perr.Error())
		}
		switch p.FormName() {
		case "file":
			if seenFile {
				return "", nil, opts, BadRequest("invalid_multipart", "Send one file.")
			}
			seenFile = true
			name = p.FileName()
			if data, perr = io.ReadAll(io.LimitReader(p, mailchimp.MaxUploadBytes+1)); perr != nil {
				if e := tooBig(perr); e != nil {
					return "", nil, opts, e
				}
				return "", nil, opts, BadRequest("invalid_multipart", "The file can't be read: "+perr.Error())
			}
			if len(data) > mailchimp.MaxUploadBytes {
				return "", nil, opts, &Error{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large",
					Message: fmt.Sprintf("The export is over %d MB.", mailchimp.MaxUploadBytes>>20)}
			}
		case "audience_name", "group_columns":
			v, verr := io.ReadAll(io.LimitReader(p, 4<<10))
			if verr != nil {
				if e := tooBig(verr); e != nil {
					return "", nil, opts, e
				}
				return "", nil, opts, BadRequest("invalid_multipart", "The form can't be read: "+verr.Error())
			}
			if p.FormName() == "audience_name" {
				audience = string(v)
			} else {
				groups = append(groups, string(v))
			}
		default:
			return "", nil, opts, BadRequest("invalid_multipart", fmt.Sprintf("The form has a part %q; the parts are file, audience_name and group_columns.", p.FormName()))
		}
	}
	if !seenFile {
		return "", nil, opts, Unprocessable("file_required", "Send the export in a part named file.")
	}
	a, g, oerr := mailchimp.CleanOptions(audience, groups)
	if oerr != nil {
		return "", nil, opts, oerr
	}
	return name, data, mailchimp.Options{AudienceName: a, GroupColumns: g}, nil
}

// CreateMailchimpBundle is POST /v1/imports/mailchimp/bundles.
func (s *Server) CreateMailchimpBundle(w http.ResponseWriter, r *http.Request) {
	c := s.mailchimpCaller(w, r)
	if c == nil {
		return
	}
	select {
	case mailchimpUploads <- struct{}{}:
		defer func() { <-mailchimpUploads }()
	case <-r.Context().Done():
		return
	}
	// An export takes longer to send than the server's usual 30 seconds.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(mailchimpUploadTime))
	_ = rc.SetWriteDeadline(time.Now().Add(mailchimpUploadTime + time.Minute))
	name, data, opts, err := readMailchimpUpload(w, r)
	if err != nil {
		var re *mailchimp.Error
		if errors.As(err, &re) {
			s.mailchimpError(w, r, err)
			return
		}
		WriteError(w, err)
		return
	}
	opts.MaxMembers = s.MailchimpMaxMembers
	b, err := mailchimp.Read(bytes.NewReader(data), int64(len(data)), name, opts)
	if s.mailchimpError(w, r, err) {
		return
	}
	u, err := s.Mailchimp.Save(r.Context(), c.AccountID, name, data, opts)
	if s.mailchimpError(w, r, err) {
		return
	}
	out, err := s.mailchimpBundleJSON(r.Context(), c.AccountID, u, b)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// loadMailchimpBundle reads an upload's export again, or nil once its file
// is gone.
func (s *Server) loadMailchimpBundle(ctx context.Context, accountID string, u *mailchimp.Upload) (*mailchimp.Bundle, error) {
	if !u.HasData {
		return nil, nil
	}
	select {
	case mailchimpUploads <- struct{}{}:
		defer func() { <-mailchimpUploads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, err := s.Mailchimp.Data(ctx, accountID, u.ID)
	if errors.Is(err, mailchimp.ErrUploadUsed) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mailchimp.Read(bytes.NewReader(data), int64(len(data)), u.FileName, u.Options(s.MailchimpMaxMembers))
}

// GetMailchimpBundle is GET /v1/imports/mailchimp/bundles/{bundle_id}.
func (s *Server) GetMailchimpBundle(w http.ResponseWriter, r *http.Request, id apigen.BundleId) {
	c := s.mailchimpCaller(w, r)
	if c == nil || !validBundleID(w, id) {
		return
	}
	u, err := s.Mailchimp.Get(r.Context(), c.AccountID, id)
	if s.mailchimpError(w, r, err) {
		return
	}
	var b *mailchimp.Bundle
	if u.ConfirmedAt == nil {
		if b, err = s.loadMailchimpBundle(r.Context(), c.AccountID, u); s.mailchimpError(w, r, err) {
			return
		}
	}
	out, err := s.mailchimpBundleJSON(r.Context(), c.AccountID, u, b)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ConfirmMailchimpBundle is POST /v1/imports/mailchimp/bundles/{bundle_id}/confirm.
func (s *Server) ConfirmMailchimpBundle(w http.ResponseWriter, r *http.Request, id apigen.BundleId) {
	c := s.mailchimpCaller(w, r)
	if c == nil || !validBundleID(w, id) {
		return
	}
	var req apigen.ConfirmMailchimpBundleRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	campaigns := mailchimp.DefaultCampaigns
	if req.Campaigns != nil {
		campaigns = *req.Campaigns
	}
	if campaigns < 1 || campaigns > mailchimp.MaxCampaigns {
		WriteError(w, Unprocessable("invalid_campaigns", fmt.Sprintf("campaigns is 1 to %d.", mailchimp.MaxCampaigns)))
		return
	}
	if !req.PermissionConfirmed {
		WriteError(w, Unprocessable("permission_required",
			"Confirm that these people agreed to get your email: Mailchimp's subscribed contacts come in subscribed, with no confirmation email.").
			WithHint("Send `permission_confirmed: true`, or `--permission-confirmed` in the CLI, once you're sure."))
		return
	}
	ctx := r.Context()
	u, err := s.Mailchimp.Get(ctx, c.AccountID, id)
	if s.mailchimpError(w, r, err) {
		return
	}
	if u.ConfirmedAt == nil {
		b, err := s.loadMailchimpBundle(ctx, c.AccountID, u)
		if s.mailchimpError(w, r, err) {
			return
		}
		if b == nil {
			s.mailchimpError(w, r, mailchimp.ErrUploadUsed)
			return
		}
		var importID *string
		if b.Members() > 0 {
			im, _, err := s.Contacts.CreateMailchimpImport(ctx, c.AccountID, contacts.MailchimpImportInput{
				Bundle: b, FileName: u.FileName, PermissionConfirmed: true, IdempotencyKey: deref(req.IdempotencyKey)})
			if s.mailchimpError(w, r, err) {
				return
			}
			importID = &im.ID
		}
		if u, err = s.Mailchimp.Confirm(ctx, c.AccountID, id, importID, campaigns, len(mailchimp.PlanEmails(b, campaigns))); s.mailchimpError(w, r, err) {
			return
		}
	}
	out, err := s.mailchimpBundleJSON(ctx, c.AccountID, u, nil)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func validBundleID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "bundle_id must be an uploaded export's id."))
		return false
	}
	return true
}

// mailchimpBundleJSON is an upload as the API shows it, with the preview of b
// when b is set.
func (s *Server) mailchimpBundleJSON(ctx context.Context, accountID string, u *mailchimp.Upload, b *mailchimp.Bundle) (apigen.MailchimpBundle, error) {
	out := apigen.MailchimpBundle{Id: u.ID, Bytes: u.Bytes, Status: "unconfirmed", CreatedAt: u.CreatedAt.UTC(),
		ExpiresAt: u.ExpiresAt.UTC(), ConfirmedAt: utcPtr(u.ConfirmedAt), ImportId: u.ImportID, Campaigns: u.Campaigns,
		Emails: mailchimpProgressJSON(u)}
	if u.FileName != "" {
		out.FileName = &u.FileName
	}
	if u.ConfirmedAt != nil {
		out.Status = "confirmed"
	}
	if b == nil {
		return out, nil
	}
	p := &apigen.MailchimpPreview{Source: apigen.MailchimpPreviewSource(b.Source), Audiences: []apigen.MailchimpAudience{},
		Emails: []apigen.MailchimpEmail{}, CampaignsAvailable: len(b.Campaigns)}
	for _, a := range b.Audiences {
		var by apigen.MailchimpStatusCounts
		for _, m := range a.Members {
			switch m.Status {
			case mailchimp.Subscribed:
				by.Subscribed++
			case mailchimp.Unsubscribed:
				by.Unsubscribed++
			case mailchimp.Cleaned:
				by.Cleaned++
			case mailchimp.Pending:
				by.Pending++
			case mailchimp.Transactional:
				by.Transactional++
			case mailchimp.Archived:
				by.Archived++
			}
		}
		p.Audiences = append(p.Audiences, apigen.MailchimpAudience{Name: a.Name, Members: len(a.Members), ByStatus: by})
	}
	for _, e := range mailchimp.PlanEmails(b, u.Campaigns) {
		p.Emails = append(p.Emails, mailchimpEmailJSON(e, nil))
	}
	p.IgnoredFiles = make([]struct {
		Path   string                                    `json:"path"`
		Reason apigen.MailchimpPreviewIgnoredFilesReason `json:"reason"`
	}, len(b.Ignored))
	for i, f := range b.Ignored {
		p.IgnoredFiles[i].Path, p.IgnoredFiles[i].Reason = f.Path, apigen.MailchimpPreviewIgnoredFilesReason(f.Reason)
	}
	for _, path := range mailchimp.TemplatesLeftOut(b) {
		p.IgnoredFiles = append(p.IgnoredFiles, struct {
			Path   string                                    `json:"path"`
			Reason apigen.MailchimpPreviewIgnoredFilesReason `json:"reason"`
		}{path, apigen.MailchimpPreviewIgnoredFilesReasonTooManyTemplates})
	}
	if b.Members() > 0 {
		cp, err := s.Contacts.PreviewMailchimpImport(ctx, accountID, b)
		if err != nil {
			return out, err
		}
		p.Contacts = mailchimpContactsJSON(cp)
	}
	out.Preview = p
	return out, nil
}

func mailchimpContactsJSON(cp *contacts.MailchimpPreview) *apigen.MailchimpContactsPreview {
	out := &apigen.MailchimpContactsPreview{ExistingTagged: cp.ExistingTagged, ExistingSuppressed: cp.ExistingSuppressed,
		TagsToCreate: cp.TagsToCreate, MergeSuppressed: cp.Summary.MergeSuppressed, MergeSuppressedExamples: cp.MergeSuppressedExamples,
		DroppedFields: cp.Summary.DroppedFields, NamesCut: cp.Summary.NamesCut, WithOptinTime: cp.Summary.WithOptinTime,
		NewestOptin: utcPtr(cp.Summary.NewestOptin), Holds: cp.Holds, Waits: cp.Waits}
	if out.DroppedFields == nil {
		out.DroppedFields = []string{}
	}
	out.New.Subscribed = cp.New[contacts.StatusSubscribed]
	out.New.Unconfirmed = cp.New[contacts.StatusUnconfirmed]
	out.New.Unsubscribed = cp.New[contacts.StatusUnsubscribed]
	out.New.Bounced = cp.New[contacts.StatusBounced]
	out.Skipped.InvalidEmail = cp.Skipped[contacts.SkipInvalidEmail]
	out.Skipped.InvalidRow = cp.Skipped[contacts.SkipInvalidRow]
	out.Skipped.Suppressed = cp.Skipped[contacts.SkipSuppressed]
	out.Skipped.NotSubscribed = cp.Skipped[contacts.SkipNotSubscribed]
	out.Skipped.Archived = cp.Skipped[contacts.SkipArchived]
	out.RenamedTags = make([]struct {
		From string `json:"from"`
		To   string `json:"to"`
	}, len(cp.Summary.RenamedTags))
	for i, t := range cp.Summary.RenamedTags {
		out.RenamedTags[i].From, out.RenamedTags[i].To = t.From, t.To
	}
	pl := cp.Plan
	out.Plan.Plan = apigen.MailchimpContactsPreviewPlanPlan(pl.Plan)
	out.Plan.Upgrade, out.Plan.Contacts, out.Plan.Limit, out.Plan.Tester = pl.Upgrade, pl.Contacts, pl.Limit, pl.Tester
	out.Plan.Units, out.Plan.MonthCents, out.Plan.YearCents = pl.Units, int(pl.MonthCents), int(pl.YearCents)
	return out
}

func mailchimpEmailJSON(e mailchimp.EmailPlan, id *string) apigen.MailchimpEmail {
	notes := func(ns []mailchimp.Note) []apigen.MailchimpEmailNote {
		out := make([]apigen.MailchimpEmailNote, len(ns))
		for i, n := range ns {
			out[i] = apigen.MailchimpEmailNote{Code: apigen.MailchimpEmailNoteCode(n.Code), Message: n.Message}
			if n.Line > 0 {
				l := n.Line
				out[i].Line = &l
			}
		}
		return out
	}
	return apigen.MailchimpEmail{From: apigen.MailchimpEmailFrom(e.From), File: e.File, Name: e.Name, Subject: e.Subject,
		PreviewText: e.PreviewText, Images: e.Images, Errors: notes(e.Errors), Warnings: notes(e.Warnings), EmailId: id}
}

func mailchimpProgressJSON(u *mailchimp.Upload) apigen.MailchimpEmailProgress {
	out := apigen.MailchimpEmailProgress{Status: apigen.MailchimpEmailProgressStatus(u.EmailsStatus), Total: u.EmailsTotal,
		Made: make([]apigen.MailchimpEmail, len(u.Emails)), Error: u.EmailsError}
	for i, e := range u.Emails {
		id := e.EmailID
		out.Made[i] = mailchimpEmailJSON(e.EmailPlan, &id)
	}
	return out
}

// importMailchimpJSON adds a Mailchimp import's upload and emails to out.
func (s *Server) importMailchimpJSON(ctx context.Context, accountID string, im *contacts.Import, out *apigen.Import) error {
	if im.Status == contacts.ImportWaiting {
		note, err := s.Contacts.WaitNote(ctx, accountID)
		if err != nil {
			return err
		}
		out.WaitingNote = &note
	}
	if im.Kind != contacts.ImportKindMailchimp || s.Mailchimp == nil {
		return nil
	}
	u, err := s.Mailchimp.ForImport(ctx, accountID, im.ID)
	if errors.Is(err, mailchimp.ErrUploadNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	out.Mailchimp = &struct {
		BundleId string                        `json:"bundle_id"`
		Emails   apigen.MailchimpEmailProgress `json:"emails"`
	}{BundleId: u.ID, Emails: mailchimpProgressJSON(u)}
	return nil
}
