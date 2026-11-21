package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// mailchimpExportDir is the synthetic account export MG-69's tests read.
const mailchimpExportDir = "../mailchimp/testdata/account_export"

// mailchimpZip zips the synthetic account export, as Mailchimp sends it.
func mailchimpZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(mailchimpExportDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(mailchimpExportDir, p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // test data
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// mailchimpUpload is an upload request for a file named name holding data,
// with the form's other fields.
func mailchimpUpload(name string, data func(*testing.T) []byte, fields ...[2]string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, f := range fields {
			_ = mw.WriteField(f[0], f[1])
		}
		w, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data(t))
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/v1/imports/mailchimp/bundles", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		return r
	}
}

var mailchimpExportUpload = mailchimpUpload("mailchimp-export.zip", mailchimpZip)

// tooLargeMailchimpUpload streams an upload over MaxMailchimpBodyBytes
// without holding it in memory.
func tooLargeMailchimpUpload(*testing.T) *http.Request {
	const boundary = "mailgoose-test-boundary"
	head := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"big.zip\"\r\n\r\n"
	body := io.MultiReader(strings.NewReader(head), io.LimitReader(zeros{}, MaxMailchimpBodyBytes+1),
		strings.NewReader("\r\n--"+boundary+"--\r\n"))
	r := httptest.NewRequest(http.MethodPost, "/v1/imports/mailchimp/bundles", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	return r
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

var addedBundles sync.Map // *testing.T -> bundle id

// addedBundle uploads the synthetic export through h and keeps its id for
// bundlePath.
func addedBundle(t *testing.T, h http.Handler) {
	t.Helper()
	rec := do(t, h, bearer(mailchimpExportUpload)(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var b struct {
		ID string `json:"id"`
	}
	decodeInto(t, rec, &b)
	addedBundles.Store(t, b.ID)
	t.Cleanup(func() { addedBundles.Delete(t) })
}

// bundlePath is a request to the test's upload, or to one that doesn't exist
// when the test added none.
func bundlePath(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedBundles.Load(t); ok {
			id = v.(string)
		}
		path := "/v1/imports/mailchimp/bundles/" + id + suffix
		if body != "" {
			return jsonRequest(method, path, body)(t)
		}
		return httptest.NewRequest(method, path, nil)
	}
}

const confirmBody = `{"permission_confirmed":true}`

// fullUploads uploads as many exports as an account may keep unconfirmed.
func fullUploads(t *testing.T, h http.Handler) {
	t.Helper()
	for range mailchimp.MaxUnconfirmedUploads {
		addedBundle(t, h)
	}
}

// confirmedBundle is addedBundle, confirmed.
func confirmedBundle(t *testing.T, h http.Handler) {
	t.Helper()
	addedBundle(t, h)
	if rec := do(t, h, bearer(bundlePath(http.MethodPost, "/confirm", confirmBody))(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
}

// waitingImport is addedImport, waiting for the paid plan.
func waitingImport(t *testing.T, h http.Handler) {
	t.Helper()
	addedImport(t, h)
	id, _ := addedImports.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE imports SET status = 'waiting' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func cancelImportPath(t *testing.T) *http.Request {
	r := importPath("/cancel")(t)
	r.Method = http.MethodPost
	return r
}

var mailchimpContractCases = map[string][]contractCase{
	"cancelImport": {
		{name: "cancelled", server: draftKey(authServer), before: waitingImport, req: bearer(cancelImportPath), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodPost, "/v1/imports/x/cancel")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(cancelImportPath), status: http.StatusNotFound},
		{name: "not waiting", server: fullKey(authServer), before: addedImport, req: bearer(cancelImportPath), status: http.StatusConflict},
		{name: "no credentials", server: authServer, req: cancelImportPath, status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(cancelImportPath), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(cancelImportPath), status: http.StatusServiceUnavailable},
	},
	"createMailchimpBundle": {
		{name: "uploaded", server: draftKey(authServer), req: bearer(mailchimpExportUpload), status: http.StatusCreated},
		{name: "not multipart", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles", `{}`)), status: http.StatusBadRequest},
		{name: "too large", server: fullKey(authServer), req: bearer(tooLargeMailchimpUpload), status: http.StatusRequestEntityTooLarge},
		{name: "not an export", server: fullKey(authServer), req: bearer(mailchimpUpload("list.csv", func(*testing.T) []byte { return []byte("name\nPat\n") })), status: http.StatusUnprocessableEntity},
		{name: "too many uploads", server: fullKey(authServer), before: fullUploads, req: bearer(mailchimpExportUpload), status: http.StatusConflict},
		{name: "no credentials", server: authServer, req: mailchimpExportUpload, status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(mailchimpExportUpload)), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(mailchimpExportUpload), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(mailchimpExportUpload), status: http.StatusServiceUnavailable},
	},
	"getMailchimpBundle": {
		{name: "found", server: draftKey(authServer), before: addedBundle, req: bearer(bundlePath(http.MethodGet, "", "")), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/imports/mailchimp/bundles/export")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(bundlePath(http.MethodGet, "", "")), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: bundlePath(http.MethodGet, "", ""), status: http.StatusUnauthorized},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(bundlePath(http.MethodGet, "", "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(bundlePath(http.MethodGet, "", "")), status: http.StatusServiceUnavailable},
	},
	"deleteMailchimpBundle": {
		{name: "deleted", server: draftKey(authServer), before: addedBundle, req: bearer(bundlePath(http.MethodDelete, "", "")), status: http.StatusNoContent},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/imports/mailchimp/bundles/export")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(bundlePath(http.MethodDelete, "", "")), status: http.StatusNotFound},
		{name: "confirmed", server: fullKey(authServer), before: confirmedBundle, req: bearer(bundlePath(http.MethodDelete, "", "")), status: http.StatusConflict},
		{name: "no credentials", server: authServer, req: bundlePath(http.MethodDelete, "", ""), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(bundlePath(http.MethodDelete, "", ""))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(bundlePath(http.MethodDelete, "", "")), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(bundlePath(http.MethodDelete, "", "")), status: http.StatusServiceUnavailable},
	},
	"confirmMailchimpBundle": {
		{name: "confirmed", server: draftKey(authServer), before: addedBundle, req: bearer(bundlePath(http.MethodPost, "/confirm", confirmBody)), status: http.StatusAccepted},
		{name: "malformed", server: fullKey(authServer), before: addedBundle, req: bearer(bundlePath(http.MethodPost, "/confirm", `{"permission_confirmed":"yes"}`)), status: http.StatusBadRequest},
		{name: "no permission", server: fullKey(authServer), before: addedBundle, req: bearer(bundlePath(http.MethodPost, "/confirm", `{"permission_confirmed":false}`)), status: http.StatusUnprocessableEntity},
		{name: "unknown", server: fullKey(authServer), req: bearer(bundlePath(http.MethodPost, "/confirm", confirmBody)), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: bundlePath(http.MethodPost, "/confirm", confirmBody), status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(bundlePath(http.MethodPost, "/confirm", confirmBody))), status: http.StatusForbidden},
		{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(bundlePath(http.MethodPost, "/confirm", confirmBody)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(bundlePath(http.MethodPost, "/confirm", confirmBody)), status: http.StatusServiceUnavailable},
	},
}

func init() {
	for op, cases := range mailchimpContractCases {
		contractCases[op] = cases
	}
}

type bundleJSON struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	ImportID *string `json:"import_id"`
	Preview  *struct {
		Source    string `json:"source"`
		Audiences []struct {
			Name    string `json:"name"`
			Members int    `json:"members"`
		} `json:"audiences"`
		Contacts *struct {
			New          map[string]int `json:"new"`
			TagsToCreate []string       `json:"tags_to_create"`
		} `json:"contacts"`
		Emails []struct {
			From string `json:"from"`
			Name string `json:"name"`
		} `json:"emails"`
		CampaignsAvailable int `json:"campaigns_available"`
	} `json:"preview"`
	Emails struct {
		Status string `json:"status"`
		Total  int    `json:"total"`
		Made   []struct {
			Name    string  `json:"name"`
			EmailID *string `json:"email_id"`
		} `json:"made"`
	} `json:"emails"`
}

func TestMailchimpImportThroughTheAPI(t *testing.T) {
	s := draftKey(authServer)(t)
	h := importRunner{s.Handler(), s.Contacts}

	rec := do(t, h, bearer(mailchimpExportUpload)(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var up bundleJSON
	decodeInto(t, rec, &up)
	if up.Status != "unconfirmed" || up.Preview == nil || up.Preview.Source != "account_export" || len(up.Preview.Audiences) != 2 ||
		up.Preview.Contacts == nil || up.Preview.Contacts.New["subscribed"] == 0 || len(up.Preview.Emails) != 3 ||
		up.Preview.CampaignsAvailable != 2 {
		t.Fatalf("upload = %s", rec.Body)
	}
	// Nothing is imported until it's confirmed.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts"))(t))
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	decodeInto(t, rec, &list)
	if len(list.Data) != 0 {
		t.Fatalf("contacts before confirm: %s", rec.Body)
	}

	// The preview comes back the same.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/bundles/"+up.ID))(t))
	var again bundleJSON
	decodeInto(t, rec, &again)
	if rec.Code != http.StatusOK || again.Preview == nil || len(again.Preview.Emails) != 3 {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+up.ID+"/confirm", `{"permission_confirmed":true,"campaigns":1}`))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	var conf bundleJSON
	decodeInto(t, rec, &conf)
	if conf.Status != "confirmed" || conf.ImportID == nil || conf.Emails.Status != "queued" || conf.Emails.Total != 2 || conf.Preview != nil {
		t.Fatalf("confirm = %s", rec.Body)
	}
	// Confirming again gives the same import.
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+up.ID+"/confirm", confirmBody))(t))
	var conf2 bundleJSON
	decodeInto(t, rec, &conf2)
	if rec.Code != http.StatusAccepted || conf2.ImportID == nil || *conf2.ImportID != *conf.ImportID {
		t.Fatalf("confirm again = %d %s", rec.Code, rec.Body)
	}

	// The API's job makes the emails, without fetching from Mailchimp.
	job := &mailchimp.EmailJob{Store: s.Mailchimp, Importer: &mailchimp.EmailImporter{Emails: s.Emails, Images: s.Images,
		Fetch: func(context.Context, string) ([]byte, error) { return nil, io.ErrUnexpectedEOF }}}
	for {
		ran, err := job.RunNext(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}

	// The import runs, and its status shows the emails.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/"+*conf.ImportID))(t))
	var im struct {
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Imported  int    `json:"imported"`
		Mailchimp *struct {
			BundleID string `json:"bundle_id"`
			Emails   struct {
				Status string `json:"status"`
				Made   []struct {
					EmailID *string `json:"email_id"`
				} `json:"made"`
			} `json:"emails"`
		} `json:"mailchimp"`
	}
	decodeInto(t, rec, &im)
	if rec.Code != http.StatusOK || im.Kind != "mailchimp" || im.Status != "done" || im.Imported == 0 || im.Mailchimp == nil ||
		im.Mailchimp.BundleID != up.ID || im.Mailchimp.Emails.Status != "done" || len(im.Mailchimp.Emails.Made) != 2 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	for _, e := range im.Mailchimp.Emails.Made {
		rec := do(t, h, bearer(request(http.MethodGet, "/v1/emails/"+*e.EmailID))(t))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"html"`) {
			t.Errorf("email %s: %d %s", *e.EmailID, rec.Code, rec.Body)
		}
	}

	// Contacts and tags are there.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts"))(t))
	decodeInto(t, rec, &list)
	if len(list.Data) == 0 {
		t.Fatalf("contacts after import: %s", rec.Body)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/tags"))(t))
	if !strings.Contains(rec.Body.String(), "audience:") {
		t.Errorf("tags = %s", rec.Body)
	}
}

func TestMailchimpUploadRefusals(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	for _, tc := range []struct {
		name string
		req  func(*testing.T) *http.Request
		code string
	}{
		{"no file", func(t *testing.T) *http.Request {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			_ = mw.WriteField("audience_name", "News")
			_ = mw.Close()
			r := httptest.NewRequest(http.MethodPost, "/v1/imports/mailchimp/bundles", &body)
			r.Header.Set("Content-Type", mw.FormDataContentType())
			return r
		}, "file_required"},
		{"bad group column", mailchimpUpload("a.csv", func(*testing.T) []byte { return []byte("Email Address\na@example.com\n") },
			[2]string{"group_columns", strings.Repeat("g", 201)}), "invalid_group_column"},
	} {
		rec := do(t, h, bearer(tc.req)(t))
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decodeInto(t, rec, &e)
		if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
}

func TestMailchimpPreviewIsStoredOnUpload(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()
	addedBundle(t, h)
	id, _ := addedBundles.Load(t)
	// The preview comes from what was stored, not from reading the file
	// again.
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE mailchimp_bundles SET data = 'not a zip' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, bearer(bundlePath(http.MethodGet, "", ""))(t))
	var b bundleJSON
	decodeInto(t, rec, &b)
	if rec.Code != http.StatusOK || b.Preview == nil || len(b.Preview.Audiences) != 2 || len(b.Preview.Emails) != 3 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
}

func TestMailchimpUploadsOneAtATime(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	// Another upload of the same account is still arriving.
	rec := do(t, h, bearer(request(http.MethodGet, "/v1/whoami"))(t))
	var who struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	decodeInto(t, rec, &who)
	mailchimpSending.Store(who.Account.ID, struct{}{})
	rec = do(t, h, bearer(mailchimpExportUpload)(t))
	mailchimpSending.Delete(who.Account.ID)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "upload_in_progress") {
		t.Fatalf("in progress: %d %s", rec.Code, rec.Body)
	}

	// Every place on the task is taken.
	old := mailchimpReceiveWait
	mailchimpReceiveWait = 10 * time.Millisecond
	for range cap(mailchimpReceiving) {
		mailchimpReceiving <- struct{}{}
	}
	rec = do(t, h, bearer(mailchimpExportUpload)(t))
	for range cap(mailchimpReceiving) {
		<-mailchimpReceiving
	}
	mailchimpReceiveWait = old
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "uploads_busy") || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}

	// Past the cap, the hint says how to free a place.
	fullUploads(t, h)
	rec = do(t, h, bearer(mailchimpExportUpload)(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "too_many_uploads") ||
		!strings.Contains(rec.Body.String(), "mailgoose import mailchimp delete") {
		t.Fatalf("too many: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, bearer(bundlePath(http.MethodDelete, "", ""))(t)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, bearer(mailchimpExportUpload)(t)); rec.Code != http.StatusCreated {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
	}
}
