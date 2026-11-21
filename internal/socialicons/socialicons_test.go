package socialicons

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// Every network a social block takes has an icon in every color it offers.
func TestEveryIcon(t *testing.T) {
	for _, color := range emails.SocialColors {
		for _, network := range emails.SocialNetworks {
			if !Has(color, network) {
				t.Errorf("no %s icon for %s", color, network)
			}
		}
	}
}

func TestURL(t *testing.T) {
	if got := URL("https://app.example/", "dark", "x"); got != "https://app.example/assets/social/v1/dark/x.png" {
		t.Errorf("URL = %s", got)
	}
	if got := URL("", "dark", "x"); got != "" {
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
	rec := get("/assets/social/v1/brand/facebook.png")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Body.Len() < 100 {
		t.Errorf("icon: %d %v, %d bytes", rec.Code, rec.Header(), rec.Body.Len())
	}
	for _, path := range []string{"/assets/social/v1/brand/", "/assets/social/v1/brand/myspace.png", "/assets/social/v1/../../socialicons.go"} {
		if rec := get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}
