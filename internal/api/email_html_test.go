package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// goodHTML is valid markup for an html email, as a JSON string.
const goodHTML = `"<html><body><p>Hi {{first_name|there}}</p><a href=\"{{unsubscribe_url}}\">Unsubscribe</a></body></html>"`

func TestImportEmailHTML(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	addedHTMLEmail(t, h)
	do := func(method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(emailPath(method, suffix, body))(t))
		return rec
	}

	// Errors come back with their lines, and nothing is saved.
	bad := `<p>Hi</p>\n<script>x()</script>\n<a href=\"javascript:go()\" onclick=\"y()\">Go</a>`
	rec := do(http.MethodPut, "/html", `{"version":1,"html":"`+bad+`"}`)
	var fail struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Issues []struct {
					Line int    `json:"line"`
					Code string `json:"code"`
				} `json:"issues"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fail); err != nil || rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad html: %d %s", rec.Code, rec.Body)
	}
	var got []string
	for _, i := range fail.Error.Details.Issues {
		got = append(got, i.Code+"@"+string(rune('0'+i.Line)))
	}
	if fail.Error.Code != "invalid_html" || strings.Join(got, " ") != "script@2 javascript_url@3 event_handler@3 missing_unsubscribe@3" ||
		!strings.HasPrefix(fail.Error.Message, "The HTML can't be saved: line 2: <script>") {
		t.Errorf("got %s %v: %s", fail.Error.Code, got, fail.Error.Message)
	}

	// Good markup saves; warnings come back with it.
	good := `<style>.r{display:flex}</style>\n<p>Hi</p><a href=\"{{unsubscribe_url}}\">Unsubscribe</a>`
	rec = do(http.MethodPut, "/html", `{"version":1,"html":"`+good+`"}`)
	var ok struct {
		Email struct {
			Version  int `json:"version"`
			Document struct {
				HTML string `json:"html"`
			} `json:"document"`
		} `json:"email"`
		Warnings []struct {
			Line int    `json:"line"`
			Code string `json:"code"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("good html: %d %s", rec.Code, rec.Body)
	}
	if ok.Email.Version != 2 || !strings.HasPrefix(ok.Email.Document.HTML, "<style>") ||
		len(ok.Warnings) != 1 || ok.Warnings[0].Line != 1 || ok.Warnings[0].Code != "unsupported_css" {
		t.Errorf("saved %+v", ok)
	}

	// replaceEmailDocument checks the markup the same way; empty is fine.
	rec = do(http.MethodPut, "/document", `{"version":2,"document":{"html":"<form></form>{{unsubscribe_url}}"}}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"invalid_html"`) {
		t.Errorf("replace with a form: %d %s", rec.Code, rec.Body)
	}
	rec = do(http.MethodPut, "/document", `{"version":2,"document":{"html":""}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("replace with empty: %d %s", rec.Code, rec.Body)
	}

	// A blocks email has no markup.
	addedEmail(t, h)
	rec = do(http.MethodPut, "/html", `{"version":1,"html":`+goodHTML+`}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"kind_mismatch"`) {
		t.Errorf("blocks email: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateHTMLEmailChecksMarkup(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/emails",
		`{"name":"x","kind":"html","document":{"html":"<iframe src=\"https://x.example\"></iframe>{{unsubscribe_url}}"}}`))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"iframe"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
