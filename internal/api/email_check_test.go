package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
)

// brokenLinks says every link is a 404 and records what it was asked.
type brokenLinks struct{ asked []string }

func (b *brokenLinks) CheckLinks(_ context.Context, urls []string) map[string]emailcheck.LinkStatus {
	b.asked = append(b.asked, urls...)
	out := map[string]emailcheck.LinkStatus{}
	for _, u := range urls {
		out[u] = emailcheck.LinkStatus{Result: emailcheck.ResultMissing, Detail: "the page answers 404 Not Found"}
	}
	return out
}

type checkAnswer struct {
	OK       bool               `json:"ok"`
	Errors   []emailcheck.Issue `json:"errors"`
	Warnings []emailcheck.Issue `json:"warnings"`
}

func (a checkAnswer) codes() (errs, warns string) {
	var e, w []string
	for _, i := range a.Errors {
		e = append(e, i.Code)
	}
	for _, i := range a.Warnings {
		w = append(w, i.Code)
	}
	return strings.Join(e, " "), strings.Join(w, " ")
}

func TestCheckEmail(t *testing.T) {
	srv := fullKey(authServer)(t)
	links := &brokenLinks{}
	srv.Links = links
	h := srv.Handler()
	do := func(req func(*testing.T) *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		return rec
	}
	check := func() checkAnswer {
		t.Helper()
		rec := do(emailPath(http.MethodPost, "/check", ""))
		var a checkAnswer
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("check: %d %s", rec.Code, rec.Body)
		}
		return a
	}

	addedEmailFrom(`{"name":"Spring","document":{"settings":{"subject":"Spring menu","from_email":"news@example.com"},"blocks":[
		{"type":"image","src":"https://cdn.example.com/a.png","alt":"","href":"https://bakery.example/menu"},
		{"type":"footer","text":""}]}}`)(t, h)

	// A new account: no postal address, an unverified domain, a broken link.
	a := check()
	errs, warns := a.codes()
	if a.OK || errs != "unverified_from missing_postal_address broken_link" || warns != "missing_alt" {
		t.Errorf("first check: ok %v, errors %q, warnings %q", a.OK, errs, warns)
	}
	if strings.Join(links.asked, " ") != "https://bakery.example/menu" {
		t.Errorf("fetched %v", links.asked)
	}

	// Fix the account: a postal address, and the domain verified.
	if rec := do(jsonRequest(http.MethodPatch, "/v1/account", `{"postal_address":"1 Main St\nSpringfield"}`)); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	rec := do(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`))
	var d struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.ID == "" {
		t.Fatalf("add domain: %d %s", rec.Code, rec.Body)
	}
	if rec := do(request(http.MethodPost, "/v1/domains/"+d.ID+"/verify")); !strings.Contains(rec.Body.String(), `"status":"verified"`) {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	srv.Links = nil // links are only looked at, not fetched
	a = check()
	if errs, warns := a.codes(); !a.OK || errs != "" || warns != "missing_alt" {
		t.Errorf("after fixes: ok %v, errors %q, warnings %q", a.OK, errs, warns)
	}

	// The account's default From counts when the email has none.
	addedEmailFrom(`{"name":"No from","document":{"settings":{"subject":"Hi"},"blocks":[{"type":"text","text":"Hi"},{"type":"footer","text":""}]}}`)(t, h)
	if errs, _ := check().codes(); errs != "missing_from" {
		t.Errorf("no from: %q", errs)
	}
	if rec := do(jsonRequest(http.MethodPatch, "/v1/account", `{"from_email":"hello@news.example.com"}`)); rec.Code != http.StatusOK {
		t.Fatalf("from: %d %s", rec.Code, rec.Body)
	}
	if a := check(); !a.OK {
		t.Errorf("account from on a subdomain: %+v", a.Errors)
	}
}

func TestCheckEmailNotFound(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(emailPath(http.MethodPost, "/check", ""))(t))
	if rec.Code != http.StatusNotFound {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
