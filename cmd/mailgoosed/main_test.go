package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

// With a CDN turned on, image URLs from before it (on the API's
// /images/) still answer, since sent emails point at them.
func TestImagesServedByTheAPIWithACDN(t *testing.T) {
	// Presigning is local: static keys, no instance metadata, no network.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		name string
		img  config.Images
	}{
		{"CDN", config.Images{Bucket: "mailgoose-test-images", BaseURL: "https://img.example"}},
		{"no CDN", config.Images{Bucket: "mailgoose-test-images", BaseURL: "https://api.example/images", ServedByAPI: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, files, err := newImages(context.Background(), config.Config{AWSRegion: "us-east-2", Images: c.img}, nil, log)
			if err != nil {
				t.Fatal(err)
			}
			if files == nil {
				t.Fatal("/images/ isn't served")
			}
			rec := httptest.NewRecorder()
			http.StripPrefix("/images", files).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/images/acc1/abc.png", nil))
			if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || !strings.Contains(loc, "mailgoose-test-images") {
				t.Errorf("%d %q", rec.Code, loc)
			}
		})
	}
}
