package api

import (
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// ExportContacts is GET /v1/contacts/export. The CSV streams as it's read,
// so an error part way can only cut the file short; it's logged.
func (s *Server) ExportContacts(w http.ResponseWriter, r *http.Request, params apigen.ExportContactsParams) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	f, ok := contactFilter(w, params.Email, params.Name, params.Status, params.TagId)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := s.Contacts.ExportCSV(r.Context(), c.AccountID, f, w); err != nil {
		s.Log.ErrorContext(r.Context(), "contact export cut short", "err", err)
	}
}

// contactDataJSON is the API's ContactData.
type contactDataJSON struct {
	Contact     apigen.Contact       `json:"contact"`
	Tags        []contactTagJSON     `json:"tags"`
	Import      *contactImportJSON   `json:"import"`
	SkippedRows []contactSkippedJSON `json:"skipped_rows"`
}

type contactTagJSON struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	AddedAt time.Time `json:"added_at"`
}

type contactImportJSON struct {
	ID                  string    `json:"id"`
	FileName            *string   `json:"file_name"`
	CreatedAt           time.Time `json:"created_at"`
	PermissionConfirmed bool      `json:"permission_confirmed"`
	Held                bool      `json:"held"`
}

type contactSkippedJSON struct {
	ImportID string  `json:"import_id"`
	FileName *string `json:"file_name"`
	Row      int     `json:"row"`
	Reason   string  `json:"reason"`
}

func contactDataBody(d *contacts.ContactData) contactDataJSON {
	out := contactDataJSON{
		Contact:     contactJSON(d.Contact),
		Tags:        make([]contactTagJSON, len(d.Tags)),
		SkippedRows: make([]contactSkippedJSON, len(d.SkippedRows)),
	}
	for i, t := range d.Tags {
		out.Tags[i] = contactTagJSON{t.ID, t.Name, t.AddedAt.UTC()}
	}
	if im := d.Import; im != nil {
		out.Import = &contactImportJSON{im.ID, im.FileName, im.CreatedAt.UTC(), im.PermissionConfirmed, im.Held}
	}
	for i, sr := range d.SkippedRows {
		out.SkippedRows[i] = contactSkippedJSON{sr.ImportID, sr.FileName, sr.Row, sr.Reason}
	}
	return out
}

// GetContactData is GET /v1/contacts/{contact_id}/data.
func (s *Server) GetContactData(w http.ResponseWriter, r *http.Request, id string) {
	c := s.contactsCaller(w, r)
	if c == nil || !validContactID(w, id) {
		return
	}
	d, err := s.Contacts.Data(r.Context(), c.AccountID, id)
	if s.contactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, contactDataBody(d))
}
