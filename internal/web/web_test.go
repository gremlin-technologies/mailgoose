package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gremlin-technologies/mailgoose/internal/web"
)

func app(t *testing.T) http.Handler {
	t.Helper()
	h, err := web.New(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>mailgoose</title>")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		".env":                 {Data: []byte("SECRET=1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestNeedsIndex(t *testing.T) {
	if _, err := web.New(fstest.MapFS{}); err == nil {
		t.Fatal("New with no index.html should fail")
	}
}

func TestServesApp(t *testing.T) {
	h := app(t)
	for _, tc := range []struct {
		target, wantBody, wantCache string
		wantStatus                  int
	}{
		{"/", "<title>mailgoose</title>", "no-cache", 200},
		{"/index.html", "<title>mailgoose</title>", "no-cache", 200},
		{"/sign-in?next=/emails", "<title>mailgoose</title>", "no-cache", 200},
		{"/device?code=BCDF-GHJK", "<title>mailgoose</title>", "no-cache", 200},
		{"/campaigns/123/report", "<title>mailgoose</title>", "no-cache", 200},
		{"/assets", "<title>mailgoose</title>", "no-cache", 200},
		{"/assets/index-abc.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "<svg/>", "no-cache", 200},
		{"/assets/index-old.js", "404 page not found", "", 404},
		{"/.env", "404 page not found", "", 404},
		{"/../.env", "404 page not found", "", 404},
	} {
		rec := get(h, http.MethodGet, tc.target)
		if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("GET %s = %d %q, want %d containing %q", tc.target, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
			t.Errorf("GET %s Cache-Control = %q, want %q", tc.target, got, tc.wantCache)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options = %q", tc.target, got)
		}
	}
}

func TestIndexHeaders(t *testing.T) {
	rec := get(app(t), http.MethodGet, "/device")
	h := rec.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("the app must refuse to be framed; got CSP %q, X-Frame-Options %q", h.Get("Content-Security-Policy"), h.Get("X-Frame-Options"))
	}
	if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestHeadAndMethods(t *testing.T) {
	h := app(t)
	if rec := get(h, http.MethodHead, "/emails"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD /emails = %d with %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := get(h, http.MethodPost, "/emails"); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST /emails = %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}
