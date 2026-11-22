package brandassets

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWordmarkURL(t *testing.T) {
	if got := WordmarkURL("https://app.example/"); got != "https://app.example/assets/brand/v1/wordmark.png" {
		t.Errorf("URL = %s", got)
	}
	if got := WordmarkURL(""); got != "" {
		t.Errorf("URL with no base = %s", got)
	}
}

func TestHandler(t *testing.T) {
	h := Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/assets/brand/v1/wordmark.png")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Body.Len() < 100 {
		t.Errorf("wordmark: %d %s %s", rec.Code, rec.Header(), rec.Body.String()[:min(rec.Body.Len(), 20)])
	}
	for _, path := range []string{"/assets/brand/v1/", "/assets/brand/v1/missing.png", "/assets/brand/v2/wordmark.png", "/assets/brand/v1/wordmark.txt"} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
