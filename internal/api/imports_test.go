package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

const importBody = `{"list_source":"form","csv":"Email,First Name\npat@example.com,Pat\nnope,Nope\n","file_name":"list.csv"}`

var addedImports sync.Map // *testing.T -> import id

// addedImport imports a two-row file through h with the test's key, runs it,
// and keeps its id for importPath.
func addedImport(t *testing.T, h http.Handler) {
	t.Helper()
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports", importBody))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	var im struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &im); err != nil {
		t.Fatal(err)
	}
	addedImports.Store(t, im.ID)
	t.Cleanup(func() { addedImports.Delete(t) })
}

// importRunner runs every queued import before each request, the way the
// worker would.
type importRunner struct {
	http.Handler
	svc *contacts.Service
}

func (ir importRunner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for {
		ran, err := ir.svc.RunNextImport(r.Context())
		if err != nil {
			panic(err)
		}
		if !ran {
			break
		}
	}
	ir.Handler.ServeHTTP(w, r)
}

// importPath is a request to /v1/imports/{the test's import}{suffix}, or to an
// import that doesn't exist when the test added none.
func importPath(suffix string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedImports.Load(t); ok {
			id = v.(string)
		}
		return httptest.NewRequest(http.MethodGet, "/v1/imports/"+id+suffix, nil)
	}
}

// tooLargeImport is a createImport request over MaxImportBodyBytes.
func tooLargeImport(t *testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/imports", `{"list_source":"form","csv":"`+strings.Repeat("x", MaxImportBodyBytes)+`"}`)(t)
}

// csvBody is a createImport body for csv.
func csvBody(csv string) string {
	b, _ := json.Marshal(map[string]string{"list_source": "form", "csv": csv})
	return string(b)
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
}

type importBodyJSON struct {
	ID              string         `json:"id"`
	Status          string         `json:"status"`
	ContactStatus   string         `json:"contact_status"`
	Imported        int            `json:"imported"`
	Skipped         int            `json:"skipped"`
	TotalRows       int            `json:"total_rows"`
	Review          string         `json:"review"`
	HoldReason      *string        `json:"hold_reason"`
	HoldMessage     *string        `json:"hold_message"`
	ListSource      *string        `json:"list_source"`
	Checks          map[string]int `json:"checks"`
	SkippedByReason map[string]int `json:"skipped_by_reason"`
	Tags            []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Columns struct {
		Email     string  `json:"email"`
		FirstName *string `json:"first_name"`
		LastName  *string `json:"last_name"`
	} `json:"columns"`
}

func TestImportThroughTheAPI(t *testing.T) {
	s := draftKey(authServer)(t)
	h := importRunner{s.Handler(), s.Contacts}
	addedTag(t, h)
	addedContact(t, h)

	body := `{"list_source":"form","csv":"E-mail;Vorname;Nachname\nana@example.com;Ana;Silva\npat@example.com;Pat;Lee\nbroken;X;Y\nana@example.com;Ana;Again\n",` +
		`"columns":{"first_name":"Vorname","last_name":"Nachname"},"tags":["VIP"],"permission_confirmed":true}`
	rec := do(t, s.Handler(), bearer(jsonRequest(http.MethodPost, "/v1/imports", body))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var queued importBodyJSON
	decodeInto(t, rec, &queued)
	if queued.Status != "queued" || queued.TotalRows != 4 || queued.ContactStatus != "subscribed" || queued.Columns.Email != "E-mail" {
		t.Errorf("queued = %+v", queued)
	}

	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/"+queued.ID))(t))
	var done importBodyJSON
	decodeInto(t, rec, &done)
	if rec.Code != http.StatusOK || done.Status != "done" || done.Imported != 1 || done.Skipped != 3 ||
		done.SkippedByReason["exists"] != 1 || done.SkippedByReason["invalid_email"] != 1 || done.SkippedByReason["duplicate"] != 1 ||
		len(done.Tags) != 1 || done.Tags[0].Name != "vip" {
		t.Fatalf("done = %d %+v", rec.Code, done)
	}

	// Skipped rows page by row.
	var rows []map[string]any
	path := "/v1/imports/" + queued.ID + "/skipped-rows?limit=2"
	for path != "" {
		rec := do(t, h, bearer(request(http.MethodGet, path))(t))
		var page struct {
			Data       []map[string]any `json:"data"`
			NextCursor *string          `json:"next_cursor"`
		}
		decodeInto(t, rec, &page)
		rows = append(rows, page.Data...)
		path = ""
		if page.NextCursor != nil {
			path = "/v1/imports/" + queued.ID + "/skipped-rows?limit=2&cursor=" + *page.NextCursor
		}
	}
	if got := fmt.Sprint(rows); got != "[map[email:pat@example.com reason:exists row:3] map[email:broken reason:invalid_email row:4] map[email:ana@example.com reason:duplicate row:5]]" {
		t.Errorf("skipped rows = %s", got)
	}

	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts?email=ana"))(t))
	if !strings.Contains(rec.Body.String(), `"status":"subscribed"`) || !strings.Contains(rec.Body.String(), `"source":"import"`) ||
		!strings.Contains(rec.Body.String(), `"name":"vip"`) {
		t.Errorf("contacts = %s", rec.Body)
	}

	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports"))(t))
	if !strings.Contains(rec.Body.String(), queued.ID) {
		t.Errorf("list = %s", rec.Body)
	}
}

func TestImportRefusals(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	for _, tc := range []struct {
		body, code string
	}{
		{`{"csv":"email\na@example.com\n"}`, "list_source_required"},
		{`{"list_source":"","csv":"email\na@example.com\n"}`, "list_source_required"},
		{`{"list_source":"other","csv":"email\na@example.com\n"}`, "list_source_detail_required"},
		{`{"list_source":"bought","csv":"email\na@example.com\n"}`, "invalid_list_source"},
		{`{"list_source":"form","csv":"name\nPat\n"}`, "no_email_column"},
		{`{"list_source":"form","csv":"email\na@example.com\n","columns":{"email":"Mail"}}`, "invalid_mapping"},
		{`{"list_source":"form","csv":"email\n"}`, "no_rows"},
		{`{"list_source":"form","csv":"email\na@example.com\n","tags":["nope"]}`, "tag_not_found"},
		{`{"list_source":"form","csv":"email\na@example.com\n","tags":[` + strings.Repeat(`"t",`, contacts.MaxImportTags) + `"t"]}`, "too_many_tags"},
	} {
		rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports", tc.body))(t))
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decodeInto(t, rec, &e)
		if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != tc.code {
			t.Errorf("%s: %d %s", tc.body, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, bearer(tooLargeImport)(t)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", rec.Code)
	}
}

// A file over the 1 MB limit other requests have goes through, with an
// Idempotency-Key too.
func TestImportTakesLargeFiles(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	var b strings.Builder
	b.WriteString("email\n")
	for i := 0; b.Len() < 3*MaxBodyBytes; i++ {
		fmt.Fprintf(&b, "someone-with-a-long-address-%d@example.com\n", i)
	}
	req := bearer(jsonRequest(http.MethodPost, "/v1/imports", csvBody(b.String())))(t)
	req.Header.Set("Idempotency-Key", "large-import")
	if rec := do(t, h, req); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestImportHeldForReview(t *testing.T) {
	s := fullKey(authServer)(t)
	if _, err := s.DB.(*pgxpool.Pool).Exec(context.Background(), `UPDATE accounts SET plan = 'paid'`); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("email\n")
	for i := range contacts.LargeFirstImport + 1 {
		fmt.Fprintf(&b, "p%d@example.com\n", i)
	}
	rec := do(t, s.Handler(), bearer(jsonRequest(http.MethodPost, "/v1/imports", csvBody(b.String())))(t))
	var im importBodyJSON
	decodeInto(t, rec, &im)
	if rec.Code != http.StatusAccepted || im.Review != "held" || im.HoldReason == nil || *im.HoldReason != "large_first_import" ||
		im.HoldMessage == nil || !strings.Contains(*im.HoldMessage, "first import") {
		t.Errorf("import = %d %+v", rec.Code, im)
	}
}

// An import with many role and throwaway addresses is held, says why, and
// shows the customer its checks but nothing about other accounts (MG-63).
func TestImportHeldByItsChecks(t *testing.T) {
	s := fullKey(authServer)(t)
	csv := "email\npat@example.com\nsam@example.com\ninfo@shop.example\nx@mailinator.com\nnope\n"
	rec := do(t, s.Handler(), bearer(jsonRequest(http.MethodPost, "/v1/imports",
		`{"list_source":"customers","csv":`+strconv.Quote(csv)+`}`))(t))
	var im importBodyJSON
	decodeInto(t, rec, &im)
	if rec.Code != http.StatusAccepted || im.Review != "held" || deref(im.HoldReason) != contacts.HoldRiskyAddresses ||
		deref(im.HoldMessage) != contacts.HoldMessage(contacts.HoldRiskyAddresses) || deref(im.ListSource) != "customers" ||
		im.Checks["invalid_rows"] != 1 || im.Checks["role_addresses"] != 1 || im.Checks["disposable_addresses"] != 1 {
		t.Errorf("import = %d %+v", rec.Code, im)
	}
	if strings.Contains(rec.Body.String(), "shared") {
		t.Errorf("the customer sees the shared-list count: %s", rec.Body)
	}
}

// Import is one more route a free account can't use to pass 500 contacts.
func TestImportAtTheFreeLimit(t *testing.T) {
	s := atContactLimit(fullKey(authServer))(t)
	h := importRunner{s.Handler(), s.Contacts}
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports", `{"list_source":"form","csv":"email\nover@example.com\n"}`))(t))
	var im importBodyJSON
	decodeInto(t, rec, &im)
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/"+im.ID))(t))
	decodeInto(t, rec, &im)
	if im.Status != "done" || im.Imported != 0 || im.SkippedByReason["over_limit"] != 1 || im.Review != "held" {
		t.Errorf("import = %+v", im)
	}
	if n, _ := s.Contacts.CountedContacts(context.Background(), firstAccount(t, s)); n != contacts.FreeContactLimit {
		t.Errorf("counted = %d", n)
	}
}

func firstAccount(t *testing.T, s *Server) string {
	t.Helper()
	var id string
	if err := s.DB.(*pgxpool.Pool).QueryRow(context.Background(), `SELECT id FROM accounts LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestImportsAreKeptPerAccount(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	mine := newTestKey(t, s, "mine@example.com", auth.ScopeFull)
	theirs := newTestKey(t, s, "theirs@example.com", auth.ScopeFull)
	as := func(k testKey, req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer "+k.secret)
		return do(t, h, req)
	}
	rec := as(theirs, jsonRequest(http.MethodPost, "/v1/imports", importBody)(t))
	var im importBodyJSON
	decodeInto(t, rec, &im)
	for _, path := range []string{"/v1/imports/" + im.ID, "/v1/imports/" + im.ID + "/skipped-rows"} {
		if rec := as(mine, httptest.NewRequest(http.MethodGet, path, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("%s from another account: %d", path, rec.Code)
		}
	}
	if rec := as(mine, httptest.NewRequest(http.MethodGet, "/v1/imports", nil)); strings.Contains(rec.Body.String(), im.ID) {
		t.Error("listing shows another account's import")
	}
}
