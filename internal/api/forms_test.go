package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/forms"
)

var addedForms sync.Map // *testing.T -> form id

// addedForm creates a form through h with the test's key and keeps its id
// for formPath.
func addedForm(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/forms", `{"name":"Footer"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add form: %d %s", rec.Code, rec.Body)
	}
	var f struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	addedForms.Store(t, f.ID)
	t.Cleanup(func() { addedForms.Delete(t) })
}

// formPath is a request to /v1/forms/{the test's form}{suffix}, or to one
// that doesn't exist when the test added none.
func formPath(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedForms.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/forms/"+id+suffix, body)(t)
		}
		return httptest.NewRequest(method, "/v1/forms/"+id+suffix, nil)
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return v
}

type formOut struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Fields []struct {
		Name     string `json:"name"`
		Label    string `json:"label"`
		Required bool   `json:"required"`
	} `json:"fields"`
	Tags []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tags"`
	ButtonText     string  `json:"button_text"`
	SuccessMessage string  `json:"success_message"`
	RedirectURL    *string `json:"redirect_url"`
	DoubleOptIn    bool    `json:"double_opt_in"`
}

func TestForms(t *testing.T) {
	srv := fullKey(authServer)(t)
	srv.WebURL = "https://app.mailgoose.example"
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return at }
	h := srv.Handler()
	do := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do(bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"Newsletter"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("tag: %d %s", rec.Code, rec.Body)
	}
	tag := decode[struct {
		ID string `json:"id"`
	}](t, rec)

	// Email needn't say it's required; the defaults fill in the rest.
	rec = do(bearer(jsonRequest(http.MethodPost, "/v1/forms",
		`{"name":"Footer","fields":[{"name":"email"},{"name":"first_name","label":"Your name"}],"tag_ids":["`+tag.ID+`"]}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	f := decode[formOut](t, rec)
	if len(f.Fields) != 2 || !f.Fields[0].Required || f.Fields[0].Label != "Email" || f.Fields[1].Label != "Your name" ||
		f.Fields[1].Required || !f.DoubleOptIn || f.ButtonText != forms.DefaultButtonText ||
		f.SuccessMessage != forms.DefaultSuccessConfirm || f.RedirectURL != nil || len(f.Tags) != 1 || f.Tags[0].Name != "Newsletter" {
		t.Errorf("created %+v", f)
	}

	rec = do(bearer(jsonRequest(http.MethodPatch, "/v1/forms/"+f.ID,
		`{"redirect_url":"https://bakery.example/thanks","double_opt_in":false,"tag_ids":[]}`))(t))
	u := decode[formOut](t, rec)
	if rec.Code != http.StatusOK || u.Name != "Footer" || u.RedirectURL == nil || u.DoubleOptIn || len(u.Tags) != 0 {
		t.Errorf("update: %d %+v", rec.Code, u)
	}

	// Each refusal names the part that's wrong.
	for body, want := range map[string]string{
		`{"redirect_url":"http://bakery.example"}`:                     "redirect_url",
		`{"fields":[{"name":"first_name"}]}`:                           "fields",
		`{"fields":[{"name":"email","required":false}]}`:               "fields[0].required",
		`{"button_text":""}`:                                           "button_text",
		`{"fields":[{"name":"email"},{"name":"email"}]}`:               "fields[1].name",
		`{"fields":[{"name":"email"},{"name":"phone"}]}`:               "", // the schema refuses it
		`{"tag_ids":["` + noSuchID + `"]}`:                             "unknown_tag",
		`{"name":"` + strings.Repeat("x", forms.MaxNameChars+1) + `"}`: "name",
	} {
		rec := do(bearer(jsonRequest(http.MethodPatch, "/v1/forms/"+f.ID, body))(t))
		var e struct {
			Error struct {
				Code    string         `json:"code"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		switch want {
		case "":
			if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
				t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
			}
		case "unknown_tag":
			if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != "unknown_tag" {
				t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
			}
		default:
			if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != "invalid_form" || e.Error.Details["field"] != want {
				t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
			}
		}
	}

	rec = do(bearer(request(http.MethodGet, "/v1/forms/"+f.ID+"/embed"))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("embed: %d %s", rec.Code, rec.Body)
	}
	e := decode[struct {
		ActionURL string `json:"action_url"`
		HTML      string `json:"html"`
		Script    string `json:"script"`
	}](t, rec)
	if e.ActionURL != "https://app.mailgoose.example/f/"+f.ID || !strings.Contains(e.HTML, `action="`+e.ActionURL+`"`) ||
		!strings.Contains(e.HTML, `value="`+formStamp(at)) || !strings.Contains(e.Script, "<script>") {
		t.Errorf("embed %+v", e)
	}

	rec = do(bearer(request(http.MethodGet, "/v1/forms"))(t))
	list := decode[struct {
		Data []formOut `json:"data"`
	}](t, rec)
	if rec.Code != http.StatusOK || len(list.Data) != 1 || list.Data[0].ID != f.ID {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(bearer(request(http.MethodGet, "/v1/forms?cursor=nope"))(t)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", rec.Code)
	}
}

// formStamp is the start of a stamp made at t: its unix seconds.
func formStamp(t time.Time) string {
	return strings.Split(forms.Stamp([]byte("k"), "f", t), ".")[0] + "."
}

func TestFormsPaging(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	for _, n := range []string{"A", "B", "C"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/forms", `{"name":"`+n+`"}`))(t))
		if rec.Code != http.StatusCreated {
			t.Fatal(rec.Body)
		}
	}
	var names []string
	cursor := ""
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/forms?limit=2"+cursor))(t))
		page := decode[struct {
			Data       []formOut `json:"data"`
			NextCursor *string   `json:"next_cursor"`
		}](t, rec)
		for _, f := range page.Data {
			names = append(names, f.Name)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if strings.Join(names, "") != "CBA" {
		t.Errorf("pages gave %v", names)
	}
}
