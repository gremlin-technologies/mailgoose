package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// MaxImportBodyBytes is the largest createImport request body: the largest
// file, with room for JSON escaping.
const MaxImportBodyBytes = contacts.MaxImportBytes + contacts.MaxImportBytes/4

// bodyLimit is the largest request body r may have.
func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/imports" {
		return MaxImportBodyBytes
	}
	return MaxBodyBytes
}

// importError writes the response for an imports error, and reports whether
// there was one.
func (s *Server) importError(w http.ResponseWriter, r *http.Request, err error) bool {
	var ve *auth.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &ve):
		e := Unprocessable(ve.Code, ve.Message)
		switch ve.Code {
		case "no_email_column", "invalid_mapping":
			e = e.WithHint("Name the columns with `columns`, or `--map email=<header>` in the CLI.")
		case "tag_not_found":
			e = e.WithHint("`mailgoose tags create <name>` makes it.")
		}
		WriteError(w, e)
	case errors.Is(err, contacts.ErrImportNotFound):
		WriteError(w, NotFound("not_found", "No import with that id in this account.").
			WithHint("`mailgoose import list` shows the account's imports."))
	default:
		s.Log.ErrorContext(r.Context(), "import request failed", "err", err)
		WriteError(w, Internal())
	}
	return true
}

func importJSON(im *contacts.Import) apigen.Import {
	out := apigen.Import{
		Id:            im.ID,
		FileName:      im.FileName,
		Status:        apigen.ImportStatus(im.Status),
		ContactStatus: apigen.ImportContactStatus(im.ContactStatus()),
		Tags:          tagRefsJSON(im.Tags),
		TotalRows:     im.TotalRows,
		ProcessedRows: im.ProcessedRows,
		Imported:      im.Imported,
		Skipped:       im.Skipped,
		Review:        apigen.ImportReview(im.Review),
		HoldReason:    im.HoldReason,
		Error:         im.Error,
		CreatedAt:     im.CreatedAt.UTC(),
		StartedAt:     utcPtr(im.StartedAt),
		FinishedAt:    utcPtr(im.FinishedAt),
	}
	out.Columns.Email = im.EmailColumn
	out.Columns.FirstName = im.FirstNameColumn
	out.Columns.LastName = im.LastNameColumn
	out.Columns.Status = im.StatusColumn
	out.Columns.Tags = im.TagsColumn
	by := im.SkippedByReason
	out.SkippedByReason.InvalidEmail = by[contacts.SkipInvalidEmail]
	out.SkippedByReason.InvalidRow = by[contacts.SkipInvalidRow]
	out.SkippedByReason.Duplicate = by[contacts.SkipDuplicate]
	out.SkippedByReason.Exists = by[contacts.SkipExists]
	out.SkippedByReason.Suppressed = by[contacts.SkipSuppressed]
	out.SkippedByReason.OverLimit = by[contacts.SkipOverLimit]
	return out
}

func validImportID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "import_id must be an import's id."))
		return false
	}
	return true
}

// CreateImport is POST /v1/imports.
func (s *Server) CreateImport(w http.ResponseWriter, r *http.Request) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateImportRequest
	if err := decodeJSON(w, r, &req, MaxImportBodyBytes); err != nil {
		WriteError(w, err)
		return
	}
	in := contacts.ImportInput{CSV: []byte(req.Csv)}
	if req.FileName != nil {
		in.FileName = *req.FileName
	}
	if req.Columns != nil {
		in.EmailColumn = deref(req.Columns.Email)
		in.FirstNameColumn = deref(req.Columns.FirstName)
		in.LastNameColumn = deref(req.Columns.LastName)
		in.StatusColumn = deref(req.Columns.Status)
		in.TagsColumn = deref(req.Columns.Tags)
	}
	if req.Tags != nil {
		if len(*req.Tags) > contacts.MaxImportTags {
			WriteError(w, Unprocessable("too_many_tags", "An import puts at most "+strconv.Itoa(contacts.MaxImportTags)+" tags on its contacts."))
			return
		}
		in.Tags = *req.Tags
	}
	if req.PermissionConfirmed != nil {
		in.PermissionConfirmed = *req.PermissionConfirmed
	}
	im, err := s.Contacts.CreateImport(r.Context(), c.AccountID, in)
	if s.importError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, importJSON(im))
}

// ListImports is GET /v1/imports.
func (s *Server) ListImports(w http.ResponseWriter, r *http.Request, _ apigen.ListImportsParams) {
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
	ims, err := s.Contacts.ListImports(r.Context(), c.AccountID, after, pr.Limit+1)
	if s.importError(w, r, err) {
		return
	}
	page := NewPage(ims, pr.Limit, func(im *contacts.Import) string {
		cur := contacts.ImportCursor(im)
		return contactCursor(&contacts.Contact{CreatedAt: cur.CreatedAt, ID: cur.ID})
	})
	out := apigen.ImportList{Data: make([]apigen.Import, len(page.Data)), NextCursor: page.NextCursor}
	for i, im := range page.Data {
		out.Data[i] = importJSON(im)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetImport is GET /v1/imports/{import_id}.
func (s *Server) GetImport(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validImportID(w, id) {
		return
	}
	im, err := s.Contacts.GetImport(r.Context(), c.AccountID, id)
	if s.importError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, importJSON(im))
}

// ListImportSkippedRows is GET /v1/imports/{import_id}/skipped-rows.
func (s *Server) ListImportSkippedRows(w http.ResponseWriter, r *http.Request, id string, _ apigen.ListImportSkippedRowsParams) {
	c := s.contactsCaller(w, r)
	if c == nil || !validImportID(w, id) {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	after := 0
	if pr.After != "" {
		if after, err = strconv.Atoi(pr.After); err != nil || after < 1 {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	rows, err := s.Contacts.ListSkippedRows(r.Context(), c.AccountID, id, after, pr.Limit+1)
	if s.importError(w, r, err) {
		return
	}
	page := NewPage(rows, pr.Limit, func(sr contacts.SkippedRow) string { return strconv.Itoa(sr.Row) })
	out := apigen.SkippedRowList{Data: make([]apigen.SkippedRow, len(page.Data)), NextCursor: page.NextCursor}
	for i, sr := range page.Data {
		out.Data[i] = apigen.SkippedRow{Row: sr.Row, Email: sr.Email, Reason: apigen.SkipReason(sr.Reason)}
	}
	writeJSON(w, http.StatusOK, out)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
