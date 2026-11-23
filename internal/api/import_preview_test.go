package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

const previewBody = `{"csv":"Email,First Name\npat@example.com,Pat\nnope,Nope\n"}`

// tooLargePreview is a previewImport request over MaxImportBodyBytes.
func tooLargePreview(t *testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/imports/preview", `{"csv":"`+strings.Repeat("x", MaxImportBodyBytes)+`"}`)(t)
}

type previewJSON struct {
	Separator string `json:"separator"`
	Rows      int    `json:"rows"`
	Columns   []struct {
		Position int      `json:"position"`
		Header   string   `json:"header"`
		Name     string   `json:"name"`
		Field    *string  `json:"field"`
		Samples  []string `json:"samples"`
	} `json:"columns"`
	Problem *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"problem"`
}

// fields is each column's name and field, as "name=field" or "name=-".
func (p previewJSON) fields() string {
	var out []string
	for _, c := range p.Columns {
		f := "-"
		if c.Field != nil {
			f = *c.Field
		}
		out = append(out, c.Name+"="+f)
	}
	return strings.Join(out, " ")
}

func (p previewJSON) problem() string {
	if p.Problem == nil {
		return ""
	}
	return p.Problem.Code
}

func TestPreviewImport(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	outer := t
	for _, tc := range []struct {
		name, body                 string
		separator, fields, problem string
		rows                       int
	}{
		{name: "comma, guessed", body: csvPreview("Email Address,fname,Surname,Notes\npat@example.com,Pat,Lee,hi\n"),
			separator: "comma", fields: "Email Address=email fname=first_name Surname=last_name Notes=-", rows: 1},
		{name: "semicolon with a BOM", body: csvPreview("\ufeffE-mail;Vorname\nana@example.com;Ana\n\nbo@example.com;Bo\n"),
			separator: "semicolon", fields: "E-mail=email Vorname=-", rows: 2},
		{name: "tab", body: csvPreview("email\tname\tstatus\tTags\na@example.com\tA\tunsubscribed\tvip\n"),
			separator: "tab", fields: "email=email name=- status=status Tags=tags", rows: 1},
		{name: "named", body: `{"csv":"mail,Vorname\na@example.com,A\n","columns":{"first_name":"Vorname"}}`,
			separator: "comma", fields: "mail=email Vorname=first_name", rows: 1},
		{name: "skipped fields aren't guessed", body: `{"csv":"email,status,tags\na@example.com,unsubscribed,vip\n","columns":{"status":"","tags":""}}`,
			separator: "comma", fields: "email=email status=- tags=-", rows: 1},
		{name: "blank and repeated headers by position", body: `{"csv":"email,,Name,Name\na@example.com,x,A,B\n","columns":{"first_name":"#4"}}`,
			separator: "comma", fields: "email=email #2=- Name=- #4=first_name", rows: 1},
		{name: "no email column", body: csvPreview("name,phone\nPat,123\n"),
			separator: "comma", fields: "name=- phone=-", problem: "no_email_column", rows: 1},
		{name: "a named column that isn't there", body: `{"csv":"email,name\na@example.com,A\n","columns":{"first_name":"Vorname"}}`,
			separator: "comma", fields: "email=email name=-", problem: "invalid_mapping", rows: 1},
		{name: "one column for two fields", body: `{"csv":"email,name\na@example.com,A\n","columns":{"first_name":"name","last_name":"name"}}`,
			separator: "comma", fields: "email=email name=first_name", problem: "invalid_mapping", rows: 1},
		{name: "email can't be skipped", body: `{"csv":"email,name\na@example.com,A\n","columns":{"email":""}}`,
			separator: "comma", fields: "email=- name=-", problem: "invalid_mapping", rows: 1},
		{name: "header only", body: csvPreview("email,name\n"), separator: "comma", fields: "email=email name=-", problem: "no_rows"},
		{name: "a bad tag", body: csvPreview("email,tags\na@example.com,\"" + strings.Repeat("x", contacts.MaxTagNameLen+1) + "\"\n"),
			separator: "comma", fields: "email=email tags=tags", problem: "invalid_tag_name", rows: 1},
		{name: "a quote that never closes", body: csvPreview("email\n\"a@example.com\nb@example.com\n"),
			separator: "comma", fields: "email=email", problem: "invalid_csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/preview", tc.body))(outer))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var p previewJSON
			decodeInto(t, rec, &p)
			if p.Separator != tc.separator || p.fields() != tc.fields || p.problem() != tc.problem || p.Rows != tc.rows {
				t.Errorf("got %s %q problem %q rows %d", p.Separator, p.fields(), p.problem(), p.Rows)
			}
		})
	}

	// Samples are the first three rows, blank rows left out, a missing cell
	// empty and an export's formula guard taken off.
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/preview",
		csvPreview("email,name\na@example.com,A\n\nb@example.com\nc@example.com,'=C\nd@example.com,D\n")))(t))
	var p previewJSON
	decodeInto(t, rec, &p)
	if got := fmt.Sprint(p.Columns[0].Samples, p.Columns[1].Samples, p.Rows); got != "[a@example.com b@example.com c@example.com] [A  =C] 4" {
		t.Errorf("samples = %s", got)
	}

	// Previewing adds no import.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports"))(t))
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("imports after previews = %s", rec.Body)
	}

	// Files that can't be read at all are errors.
	for body, code := range map[string]string{
		csvPreview(""):          "no_rows",
		csvPreview("\"email\n"): "invalid_csv",
	} {
		rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/preview", body))(t))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"`+code+`"`) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

// A field sent empty isn't imported, though its header looks like one.
func TestImportSkipsAnEmptyField(t *testing.T) {
	s := draftKey(authServer)(t)
	h := importRunner{s.Handler(), s.Contacts}
	body := `{"list_source":"form","permission_confirmed":true,"csv":"Email,Status,Tags,,Name,Name\na@example.com,unsubscribed,vip,x,Ann,Lee\n",` +
		`"columns":{"status":"","tags":"","first_name":"#5","last_name":"#6"}}`
	rec := do(t, s.Handler(), bearer(jsonRequest(http.MethodPost, "/v1/imports", body))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var queued importBodyJSON
	decodeInto(t, rec, &queued)
	if queued.Columns.FirstName == nil || *queued.Columns.FirstName != "Name" || queued.Columns.LastName == nil || *queued.Columns.LastName != "#6" {
		t.Errorf("columns = %+v", queued.Columns)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/"+queued.ID))(t))
	var done importBodyJSON
	decodeInto(t, rec, &done)
	if done.Status != "done" || done.Imported != 1 {
		t.Fatalf("done = %+v", done)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts?email=a@example.com"))(t))
	b := rec.Body.String()
	if !strings.Contains(b, `"status":"subscribed"`) || strings.Contains(b, `"name":"vip"`) ||
		!strings.Contains(b, `"first_name":"Ann"`) || !strings.Contains(b, `"last_name":"Lee"`) {
		t.Errorf("contact = %s", b)
	}
}

func csvPreview(csv string) string {
	b, _ := json.Marshal(map[string]string{"csv": csv})
	return string(b)
}
