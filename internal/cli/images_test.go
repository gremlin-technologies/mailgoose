package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageUpload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"i1","url":"https://img.example/acc/abc.png","file_name":"logo.png","content_type":"image/png","bytes":4,"created_at":"2026-11-18T12:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_draftkey"

	path := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(path, []byte("\x89PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("image", "upload", path); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if strings.TrimSpace(h.out.String()) != "https://img.example/acc/abc.png" {
		t.Errorf("output %q", h.out)
	}
	if got["file_name"] != "logo.png" || got["data"] != base64.StdEncoding.EncodeToString([]byte("\x89PNG")) {
		t.Errorf("request %v", got)
	}

	// Too big: refused before anything is sent.
	got = nil
	big := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(big, bytes.Repeat([]byte{0}, maxImageFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("image", "upload", big); code == ExitOK || !strings.Contains(h.errOut.String(), "over 5 MB") || got != nil {
		t.Errorf("too big: exit %d, %q, sent %v", code, h.errOut, got)
	}
	if code := h.run("image", "upload", filepath.Join(t.TempDir(), "missing.png")); code == ExitOK {
		t.Error("a missing file uploaded")
	}
}

func TestImageList(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[{"id":"i1","url":"https://img.example/acc/abc.png","file_name":"logo.png","content_type":"image/png","bytes":20480,"created_at":"2026-11-18T12:00:00Z"},
			{"id":"i2","url":"https://img.example/acc/def.jpg","file_name":"","content_type":"image/jpeg","bytes":3145728,"created_at":"2026-11-18T11:00:00Z"}],"next_cursor":"c2"}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	if code := h.run("image", "list", "--limit", "2"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"logo.png", "20 KB", "3.0 MB", "https://img.example/acc/def.jpg", "mailgoose image list --cursor c2"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if query != "limit=2" {
		t.Errorf("query %q", query)
	}
}
