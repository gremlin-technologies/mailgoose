package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Importing a Mailchimp export (MG-72): upload, preview, confirm.

const mailchimpUploadPath = "/v1/imports/mailchimp/bundles"

// MaxMailchimpBodyBytes is the largest upload request: the largest export
// with room for the form's other parts.
const MaxMailchimpBodyBytes = mailchimp.MaxUploadBytes + 1<<20

// mailchimpUploadTime is how long an upload may take to arrive.
const mailchimpUploadTime = 5 * time.Minute

// mailchimpReceiving limits how many uploads one API task takes in at once,
// since each is held in memory as it arrives. Reading one takes
// mailchimp.HoldRead as well.
var mailchimpReceiving = make(chan struct{}, 2)

// mailchimpReceiveWait is how long an upload waits for a place before it's
// refused as busy.
var mailchimpReceiveWait = 20 * time.Second

// mailchimpSending is the accounts with an upload arriving on this task: an
// account sends one at a time.
var mailchimpSending sync.Map

var errMailchimpBusy = (&Error{Status: http.StatusServiceUnavailable, Code: "uploads_busy",
	Message: "Too many Mailchimp exports are arriving just now."}).WithHint("Try again in a minute.")

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
		switch ve.Code {
		case "permission_required":
			e = e.WithHint("Send `permission_confirmed: true`, or `--permission-confirmed` in the CLI, once you're sure.")
		case "invalid_mapping":
			e = e.WithHint("Name merge fields as the preview's `merge_fields[].name` has them.")
		case "unknown_field":
			e = e.WithHint("`mailgoose fields list` shows the account's fields and their keys.")
		case "field_exists":
			e = e.WithHint("Put the merge field into the existing field with `field_columns`, or `--field <key>=<merge field>` in the CLI.")
		case "field_limit":
			e = e.WithHint("Put merge fields into existing fields instead, leave some out, or delete a field you no longer use.")
		}
		WriteError(w, e)
	case errors.Is(err, mailchimp.ErrUploadNotFound):
		WriteError(w, NotFound("not_found", "No uploaded Mailchimp export with that id in this account; uploads are kept for 7 days.").
			WithHint("`mailgoose import mailchimp upload <file>` uploads it again."))
	case errors.Is(err, mailchimp.ErrTooManyUploads):
		WriteError(w, Conflict("too_many_uploads", fmt.Sprintf("This account already has %d uploaded exports waiting to be confirmed.", mailchimp.MaxUnconfirmedUploads)).
			WithHint("Confirm or delete one first: `mailgoose import mailchimp delete <id>`. Unconfirmed uploads are deleted after 7 days."))
	case errors.Is(err, mailchimp.ErrUploadConfirmed):
		WriteError(w, Conflict("upload_confirmed", "This export was confirmed, so it stays with its import."))
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
			// Sized from the request, so reading doesn't hold growing copies.
			buf := bytes.NewBuffer(make([]byte, 0, min(max(r.ContentLength, 0), MaxMailchimpBodyBytes)+bytes.MinRead))
			_, perr = buf.ReadFrom(io.LimitReader(p, mailchimp.MaxUploadBytes+1))
			if data = buf.Bytes(); perr != nil {
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
	ctx := r.Context()
	if n, err := s.Mailchimp.Unconfirmed(ctx, c.AccountID); s.mailchimpError(w, r, err) {
		return
	} else if n >= mailchimp.MaxUnconfirmedUploads {
		s.mailchimpError(w, r, mailchimp.ErrTooManyUploads)
		return
	}
	if _, busy := mailchimpSending.LoadOrStore(c.AccountID, struct{}{}); busy {
		WriteError(w, Conflict("upload_in_progress", "Another Mailchimp export for this account is still arriving.").
			WithHint("Wait for it to finish, then upload again."))
		return
	}
	defer mailchimpSending.Delete(c.AccountID)
	wait := time.NewTimer(mailchimpReceiveWait)
	defer wait.Stop()
	select {
	case mailchimpReceiving <- struct{}{}:
		defer func() { <-mailchimpReceiving }()
	case <-wait.C:
		w.Header().Set("Retry-After", "60")
		WriteError(w, errMailchimpBusy)
		return
	case <-ctx.Done():
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
	release, err := mailchimp.HoldRead(ctx)
	if err != nil {
		return
	}
	defer release()
	b, err := mailchimp.Read(bytes.NewReader(data), int64(len(data)), name, opts)
	if s.mailchimpError(w, r, err) {
		return
	}
	// The preview is worked out once, here, and stored with the file.
	p, err := s.mailchimpPreviewJSON(ctx, c.AccountID, b, mailchimp.DefaultCampaigns)
	if s.mailchimpError(w, r, err) {
		return
	}
	raw, err := json.Marshal(p)
	if s.mailchimpError(w, r, err) {
		return
	}
	u, err := s.Mailchimp.Save(ctx, c.AccountID, name, data, raw, opts)
	if s.mailchimpError(w, r, err) {
		return
	}
	release()
	out, err := s.mailchimpBundleJSON(ctx, u)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// loadMailchimpBundle reads an upload's export again, holding the read slot
// until release is called, or nil once its file is gone.
func (s *Server) loadMailchimpBundle(ctx context.Context, accountID string, u *mailchimp.Upload) (b *mailchimp.Bundle, release func(), err error) {
	release = func() {}
	if !u.HasData {
		return nil, release, nil
	}
	if release, err = mailchimp.HoldRead(ctx); err != nil {
		return nil, func() {}, err
	}
	data, err := s.Mailchimp.Data(ctx, accountID, u.ID)
	if errors.Is(err, mailchimp.ErrUploadUsed) {
		return nil, release, nil
	}
	if err != nil {
		return nil, release, err
	}
	b, err = mailchimp.Read(bytes.NewReader(data), int64(len(data)), u.FileName, u.Options(s.MailchimpMaxMembers))
	return b, release, err
}

// GetMailchimpBundle is GET /v1/imports/mailchimp/bundles/{bundle_id}.
func (s *Server) GetMailchimpBundle(w http.ResponseWriter, r *http.Request, id apigen.BundleId) {
	c := s.mailchimpCaller(w, r)
	if c == nil || !validBundleID(w, id) {
		return
	}
	ctx := r.Context()
	u, err := s.Mailchimp.Get(ctx, c.AccountID, id)
	if s.mailchimpError(w, r, err) {
		return
	}
	out, err := s.mailchimpBundleJSON(ctx, u)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteMailchimpBundle is DELETE /v1/imports/mailchimp/bundles/{bundle_id}.
func (s *Server) DeleteMailchimpBundle(w http.ResponseWriter, r *http.Request, id apigen.BundleId) {
	c := s.mailchimpCaller(w, r)
	if c == nil || !validBundleID(w, id) {
		return
	}
	if s.mailchimpError(w, r, s.Mailchimp.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		b, release, err := s.loadMailchimpBundle(ctx, c.AccountID, u)
		defer release()
		if s.mailchimpError(w, r, err) {
			return
		}
		if b == nil {
			s.mailchimpError(w, r, mailchimp.ErrUploadUsed)
			return
		}
		drafts, aerr := mailchimpFormDrafts(b, req.FormTags)
		if aerr != nil {
			WriteError(w, aerr)
			return
		}
		var importID *string
		if b.Members() > 0 {
			im, _, err := s.Contacts.CreateMailchimpImport(ctx, c.AccountID, contacts.MailchimpImportInput{
				Bundle: b, FileName: u.FileName, PermissionConfirmed: true, IdempotencyKey: deref(req.IdempotencyKey),
				Fields: mailchimpFieldInput(req.FieldColumns, req.NewFields)})
			if s.mailchimpError(w, r, err) {
				return
			}
			importID = &im.ID
		}
		// The forms are made before the bundle is marked confirmed, so a
		// confirm cut short makes them on the next try; making them again
		// finds the ones already made.
		made, err := s.Forms.MailchimpDrafts(ctx, c.AccountID, drafts)
		var inv *forms.InvalidError
		if errors.As(err, &inv) {
			WriteError(w, Unprocessable("too_many_form_tags", inv.Message))
			return
		}
		if s.mailchimpError(w, r, err) {
			return
		}
		bundleForms := make([]mailchimp.BundleForm, len(made))
		for i, f := range made {
			bundleForms[i] = mailchimp.BundleForm{ID: f.ID, Name: f.Name, Audience: f.Audience, New: f.New}
		}
		if u, err = s.Mailchimp.Confirm(ctx, c.AccountID, id, importID, campaigns, len(mailchimp.PlanEmails(b, campaigns)), bundleForms); s.mailchimpError(w, r, err) {
			return
		}
		// A fetched bundle holds all the import needs, so the Mailchimp
		// connection that read it is forgotten (MG-73's rule).
		if u.Fetched && s.MailchimpConnections != nil {
			if err := s.MailchimpConnections.Forget(context.WithoutCancel(ctx), c.AccountID); err != nil {
				s.Log.ErrorContext(ctx, "forgetting the Mailchimp connection after an import failed", "err", err)
			}
		}
	}
	out, err := s.mailchimpBundleJSON(ctx, u)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// mailchimpGroups are the group tags of an audience's members, sorted and
// without repeats.
func mailchimpGroups(a *mailchimp.Audience) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range a.Members {
		for _, g := range m.Groups {
			t := contacts.MailchimpGroupTag(g)
			if t != "" && !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// mailchimpFormDrafts are the signup forms the bundle's audiences get
// (MG-76): one for each audience with members, with its audience tag when
// there's more than one audience, and the requested group tags it has.
func mailchimpFormDrafts(b *mailchimp.Bundle, formTags *[]string) ([]forms.MailchimpDraft, *Error) {
	var want []string
	if formTags != nil {
		want = *formTags
	}
	var drafts []forms.MailchimpDraft
	found := map[string]bool{}
	for _, a := range b.Audiences {
		if len(a.Members) == 0 {
			continue
		}
		d := forms.MailchimpDraft{Audience: a.Name}
		if len(b.Audiences) > 1 {
			if t := contacts.MailchimpAudienceTag(a.Name); t != "" {
				d.Tags = append(d.Tags, t)
			}
		}
		groups := mailchimpGroups(a)
		for _, w := range want {
			k := strings.ToLower(strings.TrimSpace(w))
			i := slices.IndexFunc(groups, func(g string) bool { return strings.ToLower(g) == k })
			if i >= 0 && !slices.Contains(d.Tags, groups[i]) {
				d.Tags = append(d.Tags, groups[i])
				found[k] = true
			}
		}
		if len(d.Tags) > forms.MaxTags {
			return nil, Unprocessable("too_many_form_tags", fmt.Sprintf(
				"A form puts up to %d tags on people, and the form for %s would put %d. Pick fewer form_tags.", forms.MaxTags, a.Name, len(d.Tags)))
		}
		drafts = append(drafts, d)
	}
	for _, w := range want {
		if !found[strings.ToLower(strings.TrimSpace(w))] {
			return nil, Unprocessable("unknown_form_tag", fmt.Sprintf(
				"%q isn't a group of the export's audiences.", w)).WithHint("The preview's audiences[].groups lists the groups each audience has.")
		}
	}
	return drafts, nil
}

func validBundleID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "bundle_id must be an uploaded export's id."))
		return false
	}
	return true
}

// mailchimpFieldInput is a confirm request's merge field choices (MG-132).
func mailchimpFieldInput(cols *apigen.ImportFieldColumns, news *apigen.ImportNewFields) contacts.MailchimpFieldInput {
	var in contacts.MailchimpFieldInput
	if cols != nil {
		in.FieldColumns = *cols
	}
	if news != nil {
		nf := make([]contacts.NewImportField, len(*news))
		for i, n := range *news {
			nf[i] = contacts.NewImportField{Column: n.Column, Name: deref(n.Name)}
		}
		in.NewFields = &nf
	}
	return in
}

// mailchimpBundleJSON is an upload as the API shows it, with the preview
// stored on upload while it's unconfirmed. What its merge fields would
// become is worked out again, against the account's fields as they are.
func (s *Server) mailchimpBundleJSON(ctx context.Context, u *mailchimp.Upload) (apigen.MailchimpBundle, error) {
	out := apigen.MailchimpBundle{Id: u.ID, Bytes: u.Bytes, Status: "unconfirmed", CreatedAt: u.CreatedAt.UTC(),
		ExpiresAt: u.ExpiresAt.UTC(), ConfirmedAt: utcPtr(u.ConfirmedAt), ImportId: u.ImportID, Campaigns: u.Campaigns,
		Emails: mailchimpProgressJSON(u), Forms: make([]apigen.MailchimpBundleForm, len(u.Forms))}
	for i, f := range u.Forms {
		out.Forms[i] = apigen.MailchimpBundleForm{Id: f.ID, Name: f.Name, Audience: f.Audience, New: f.New}
	}
	if u.FileName != "" {
		out.FileName = &u.FileName
	}
	if u.ConfirmedAt != nil {
		out.Status = "confirmed"
	}
	if u.ConfirmedAt == nil && u.Preview != nil {
		var p apigen.MailchimpPreview
		if err := json.Unmarshal(u.Preview, &p); err != nil {
			return out, fmt.Errorf("read stored mailchimp preview: %w", err)
		}
		// A preview stored before MG-76 has no groups.
		for i := range p.Audiences {
			if p.Audiences[i].Groups == nil {
				p.Audiences[i].Groups = []string{}
			}
		}
		if c := p.Contacts; c != nil {
			// A preview stored before MG-132 has no merge fields; its
			// merge fields are in dropped_fields.
			merge := make([]contacts.MailchimpMergeField, len(c.MergeFields))
			for i, m := range c.MergeFields {
				merge[i] = contacts.MailchimpMergeField{Name: m.Name, Audiences: m.Audiences, Members: m.Members, Samples: m.Samples}
			}
			fp, err := s.Contacts.PlanMailchimpFields(ctx, u.AccountID, merge, contacts.MailchimpFieldInput{})
			if err != nil {
				return out, err
			}
			setMergeFieldsJSON(c, fp)
		}
		out.Preview = &p
	}
	return out, nil
}

// mailchimpPreviewJSON is the preview of b, against the account as it is now.
func (s *Server) mailchimpPreviewJSON(ctx context.Context, accountID string, b *mailchimp.Bundle, campaigns int) (*apigen.MailchimpPreview, error) {
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
		p.Audiences = append(p.Audiences, apigen.MailchimpAudience{Name: a.Name, Members: len(a.Members), ByStatus: by,
			Groups: mailchimpGroups(a)})
	}
	for _, e := range mailchimp.PlanEmails(b, campaigns) {
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
			return nil, err
		}
		p.Contacts = mailchimpContactsJSON(cp)
	}
	return p, nil
}

func mailchimpContactsJSON(cp *contacts.MailchimpPreview) *apigen.MailchimpContactsPreview {
	out := &apigen.MailchimpContactsPreview{ExistingTagged: cp.ExistingTagged, ExistingSuppressed: cp.ExistingSuppressed,
		TagsToCreate: cp.TagsToCreate, MergeSuppressed: cp.Summary.MergeSuppressed, MergeSuppressedExamples: cp.MergeSuppressedExamples,
		DroppedFields: cp.Summary.DroppedFields, NamesCut: cp.Summary.NamesCut, WithOptinTime: cp.Summary.WithOptinTime,
		NewestOptin: utcPtr(cp.Summary.NewestOptin), Holds: cp.Holds, Waits: cp.Waits}
	if out.DroppedFields == nil {
		out.DroppedFields = []string{}
	}
	setMergeFieldsJSON(out, cp.Fields)
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

// setMergeFieldsJSON puts what the merge fields would become on out.
func setMergeFieldsJSON(out *apigen.MailchimpContactsPreview, fp *contacts.MailchimpFieldPlan) {
	out.MergeFields = make([]apigen.MailchimpMergeField, len(fp.Choices))
	for i, c := range fp.Choices {
		m := apigen.MailchimpMergeField{Name: c.Name, Audiences: c.Audiences, Members: c.Members, Samples: c.Samples}
		if m.Audiences == nil {
			m.Audiences = []string{}
		}
		if m.Samples == nil {
			m.Samples = []string{}
		}
		if c.Field != nil {
			m.CustomField = &apigen.ImportFieldRef{Id: c.Field.ID, Key: c.Field.Key, Name: c.Field.Name}
		}
		if c.NewField != nil {
			m.NewField = &apigen.ImportNewFieldPreview{Name: c.NewField.Name, Key: c.NewField.Key}
		}
		if c.SkipReason != "" {
			r := apigen.MailchimpMergeFieldSkipReason(c.SkipReason)
			m.SkipReason = &r
		}
		out.MergeFields[i] = m
	}
	out.Fields.Limit, out.Fields.Existing, out.Fields.New, out.Fields.LeftOut = fp.Limit, fp.Existing, fp.New, fp.LeftOut
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
