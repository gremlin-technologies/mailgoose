package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPreviewEmail(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	get := func(suffix string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(emailPath(http.MethodGet, suffix, ""))(t))
		return rec
	}

	addedEmail(t, h)
	rec := get("/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("desktop: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"<!DOCTYPE html>", "Your postal address goes here.<br>Add it in Settings before you send.", `href="#unsubscribe"`} {
		if !strings.Contains(body, want) {
			t.Errorf("desktop lacks %q", want)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
		t.Errorf("csp %q", csp)
	}

	// The account's own address replaces the placeholder.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPatch, "/v1/account", `{"postal_address":"Pat's Bakery\n12 Main St"}`))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("set address: %d %s", rec.Code, rec.Body)
	}
	if body := get("/preview").Body.String(); !strings.Contains(body, "Pat&#39;s Bakery<br>12 Main St") || strings.Contains(body, "goes here") {
		t.Error("the account's address isn't in the footer")
	}

	// Mobile wraps the email in a 375px frame.
	rec = get("/preview?width=mobile")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<iframe title="Email at 375 pixels" srcdoc="&lt;!DOCTYPE html&gt;`) ||
		!strings.Contains(rec.Body.String(), "width:375px") {
		t.Errorf("mobile: %d %.300s", rec.Code, rec.Body)
	}

	// An html email is previewed with its sample name, and refused when empty.
	addedHTMLEmail(t, h)
	if body := get("/preview").Body.String(); !strings.Contains(body, "<p>Hi</p>") || !strings.Contains(body, "12 Main St</div>") {
		t.Errorf("html email: %s", body)
	}
	addedEmailFrom(`{"name":"Empty","kind":"html"}`)(t, h)
	if rec := get("/preview"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"empty_email"`) {
		t.Errorf("empty html: %d %s", rec.Code, rec.Body)
	}

	// Markup saved before a check was tightened is refused as invalid_html.
	addedEmailFrom(`{"name":"Legacy","kind":"html","document":{"html":"<p>Hi</p><a href=\"{{unsubscribe_url}}\">Unsubscribe</a>"}}`)(t, h)
	v, _ := testDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `UPDATE emails SET document = jsonb_set(document, '{html}', '"<p>Unsubscribe: {{unsubscribe_url}}</p>"') WHERE name = 'Legacy'`); err != nil {
		t.Fatal(err)
	}
	if rec := get("/preview"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"invalid_html"`) ||
		!strings.Contains(rec.Body.String(), `"code":"missing_unsubscribe"`) {
		t.Errorf("legacy html: %d %s", rec.Code, rec.Body)
	}

	// Every problem is listed.
	addedEmailFrom(`{"name":"Typos","document":{"settings":{"subject":"{{first}}"},"blocks":[{"type":"heading","text":"Hi {{name}}"}]}}`)(t, h)
	rec = get("/preview")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"path":"settings.subject"`) ||
		!strings.Contains(rec.Body.String(), `"path":"blocks[0].text"`) {
		t.Errorf("problems: %d %s", rec.Code, rec.Body)
	}
}
