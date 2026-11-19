package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/images"
)

const testImagesURL = "https://img.mailgoose.test"

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// pngUpload is an uploadImage body with a small PNG.
var pngUpload = `{"data":"` + base64.StdEncoding.EncodeToString(pngBytes) + `","file_name":"logo.png"}`

// tooLargeImage is an uploadImage request over MaxImageBodyBytes.
func tooLargeImage(*testing.T) *http.Request {
	body := `{"data":"` + strings.Repeat("A", MaxImageBodyBytes) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/images", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func imageStore(t *testing.T, s *Server) *images.Memory {
	t.Helper()
	return s.Images.Store.(*images.Memory)
}

func TestUploadImage(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/images", pngUpload))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	keys := imageStore(t, s).Keys()
	if len(keys) != 1 || raw["url"] != testImagesURL+"/"+keys[0] || raw["content_type"] != "image/png" ||
		raw["file_name"] != "logo.png" || raw["bytes"] != float64(len(pngBytes)) {
		t.Errorf("upload answered %v; stored %v", raw, keys)
	}

	// A file that isn't an image says what it looks like.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/images", `{"data":"JVBERi0xLjcK","file_name":"x.png"}`))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"detected":"application/pdf"`) {
		t.Errorf("pdf: %d %s", rec.Code, rec.Body)
	}

	// The store failing is a 503, and nothing is listed.
	imageStore(t, s).Err = errors.New("down")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/images", pngUpload))(t))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("store down: %d %s", rec.Code, rec.Body)
	}
	imageStore(t, s).Err = nil

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/images?limit=1"))(t))
	var page struct {
		Data       []map[string]any `json:"data"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Data) != 1 || page.NextCursor != nil {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestDeleteAccountRemovesImages(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	send(bearer(jsonRequest(http.MethodPost, "/v1/images", pngUpload)))(t, h)
	if n := len(imageStore(t, s).Keys()); n != 1 {
		t.Fatalf("%d images", n)
	}
	name := "Test"

	// The store failing leaves the account whole.
	imageStore(t, s).Err = errors.New("down")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodDelete, "/v1/account?confirm="+name))(t))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store down: %d %s", rec.Code, rec.Body)
	}
	imageStore(t, s).Err = nil
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodDelete, "/v1/account?confirm="+name))(t))
	if rec.Code != http.StatusNoContent || len(imageStore(t, s).Keys()) != 0 {
		t.Errorf("delete: %d %s; left %v", rec.Code, rec.Body, imageStore(t, s).Keys())
	}
}

func TestLocalImageFiles(t *testing.T) {
	dir := &images.Dir{Path: t.TempDir()}
	s := newServer(nil)
	s.ImageFiles = dir.Handler()
	if err := dir.Put(t.Context(), "acc/abc.png", "image/png", pngBytes); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/images/acc/abc.png", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(pngBytes) {
		t.Errorf("served %d %q", rec.Code, rec.Body)
	}
	// Only GET and HEAD go to the files.
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/images/acc/abc.png", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("POST served a file")
	}
}
