package images

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

// The smallest real files of each kind, enough for content sniffing.
var (
	pngData  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegData = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifData  = []byte("GIF89a\x01\x00\x01\x00")
	webpData = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
)

func TestKind(t *testing.T) {
	for want, data := range map[string][]byte{"image/png": pngData, "image/jpeg": jpegData, "image/gif": gifData, "image/webp": webpData} {
		if ct, _, err := Kind(data); err != nil || ct != want {
			t.Errorf("%s: %q %v", want, ct, err)
		}
	}
	for name, data := range map[string][]byte{
		"pdf":  []byte("%PDF-1.7\n"),
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"html": []byte("<html><script>alert(1)</script>"),
		"text": []byte("hello"),
	} {
		var un *UnsupportedError
		if _, _, err := Kind(data); !errors.As(err, &un) || un.Detected == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"logo.png": "logo.png", "../../etc/passwd": "passwd", `C:\Users\pat\logo.png`: "logo.png", "": "", "  ": "",
		strings.Repeat("é", 250): strings.Repeat("é", 200), "bad\xffname": "",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

type harness struct {
	db        *pgxpool.Pool
	store     *Memory
	svc       *Service
	acc, acc2 string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db, store: &Memory{}}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h.svc = &Service{DB: db, Store: h.store, BaseURL: "https://img.example/", Now: func() time.Time {
		now = now.Add(time.Second)
		return now
	}}
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestUpload(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img, err := h.svc.Upload(ctx, h.acc, "../Logo.PNG", pngData)
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`^` + h.acc + `/[0-9a-f]{32}\.png$`)
	keys := h.store.Keys()
	if len(keys) != 1 || !key.MatchString(keys[0]) {
		t.Fatalf("stored %v", keys)
	}
	if img.URL != "https://img.example/"+keys[0] || img.FileName != "Logo.PNG" || img.ContentType != "image/png" || img.Bytes != len(pngData) || img.ID == "" {
		t.Errorf("image %+v", img)
	}
	if !bytes.Equal(h.store.Files[keys[0]], pngData) {
		t.Error("stored bytes differ")
	}

	// The name says JPEG; the content decides.
	img2, err := h.svc.Upload(ctx, h.acc, "photo.jpg", gifData)
	if err != nil || img2.ContentType != "image/gif" || !strings.HasSuffix(img2.URL, ".gif") {
		t.Fatalf("gif named .jpg: %+v %v", img2, err)
	}
	if img2.URL == img.URL {
		t.Error("two uploads share a name")
	}

	// Refusals store nothing.
	var un *UnsupportedError
	for name, c := range map[string]struct {
		data []byte
		ok   func(error) bool
	}{
		"empty":     {nil, func(err error) bool { return errors.Is(err, ErrEmpty) }},
		"too large": {append(append([]byte{}, pngData...), make([]byte, MaxBytes)...), func(err error) bool { return errors.Is(err, ErrTooLarge) }},
		"pdf named png": {[]byte("%PDF-1.7\n"), func(err error) bool {
			return errors.As(err, &un) && un.Detected == "application/pdf"
		}},
	} {
		if _, err := h.svc.Upload(ctx, h.acc, "x.png", c.data); !c.ok(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Exactly 5 MB is allowed.
	if _, err := h.svc.Upload(ctx, h.acc, "", append(append([]byte{}, pngData...), make([]byte, MaxBytes-len(pngData))...)); err != nil {
		t.Errorf("5 MB: %v", err)
	}
	if n := len(h.store.Keys()); n != 3 {
		t.Errorf("%d files stored", n)
	}

	// A store that fails leaves no row.
	h.store.Err = errors.New("s3 down")
	if _, err := h.svc.Upload(ctx, h.acc, "", pngData); err == nil {
		t.Error("upload with a failing store")
	}
	h.store.Err = nil
	list, err := h.svc.List(ctx, h.acc, nil, 10)
	if err != nil || len(list) != 3 {
		t.Errorf("%d rows (%v)", len(list), err)
	}
}

func TestListAndDeleteAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, acc := range []string{h.acc, h.acc, h.acc, h.acc2} {
		if _, err := h.svc.Upload(ctx, acc, "", jpegData); err != nil {
			t.Fatal(err)
		}
	}
	first, err := h.svc.List(ctx, h.acc, nil, 2)
	if err != nil || len(first) != 2 || !first[0].CreatedAt.After(first[1].CreatedAt) {
		t.Fatalf("first page %+v %v", first, err)
	}
	rest, err := h.svc.List(ctx, h.acc, &Cursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}, 2)
	if err != nil || len(rest) != 1 || rest[0].ID == first[0].ID || rest[0].ID == first[1].ID {
		t.Fatalf("second page %+v %v", rest, err)
	}
	if other, _ := h.svc.List(ctx, h.acc2, nil, 10); len(other) != 1 {
		t.Errorf("other account sees %d", len(other))
	}

	if err := h.svc.DeleteAccount(ctx, h.acc); err != nil {
		t.Fatal(err)
	}
	keys := h.store.Keys()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], h.acc2+"/") {
		t.Errorf("left %v", keys)
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if err := h.svc.DeleteAccount(ctx, bad); err == nil {
			t.Errorf("deleted %q", bad)
		}
	}
}

func TestDir(t *testing.T) {
	ctx := context.Background()
	d := &Dir{Path: t.TempDir()}
	if err := d.Put(ctx, "acc1/abc.png", "image/png", pngData); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "acc2/def.png", "image/png", pngData); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../escape.png", "/abs.png", ""} {
		if err := d.Put(ctx, bad, "image/png", pngData); err == nil {
			t.Errorf("stored %q", bad)
		}
	}

	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/acc1/abc.png")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, pngData) || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("served %d %q %v", resp.StatusCode, body, resp.Header)
	}
	for _, path := range []string{"/", "/acc1/", "/acc1/missing.png"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}

	if err := d.DeletePrefix(ctx, "acc1/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Path, "acc1")); !os.IsNotExist(err) {
		t.Errorf("acc1 still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.Path, "acc2", "def.png")); err != nil {
		t.Errorf("acc2 gone: %v", err)
	}
	if err := d.DeletePrefix(ctx, "missing/"); err != nil {
		t.Errorf("missing prefix: %v", err)
	}
}

func TestTooMany(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `INSERT INTO images (account_id, key, content_type, bytes)
		SELECT $1::uuid, $1::text || '/' || g || '.png', 'image/png', 1 FROM generate_series(1, $2) g`, h.acc, MaxPerAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Upload(ctx, h.acc, "", pngData); !errors.Is(err, ErrTooMany) {
		t.Errorf("1,001st: %v", err)
	}
	if len(h.store.Keys()) != 0 {
		t.Error("stored a refused image")
	}
	if _, err := h.svc.Upload(ctx, h.acc2, "", pngData); err != nil {
		t.Errorf("other account: %v", err)
	}
}

func TestS3HandlerRedirects(t *testing.T) {
	// Presigning is local: static keys and no network.
	client := s3.New(s3.Options{Region: "us-east-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")})
	b := &S3{Client: client, Bucket: "mailgoose-test-images"}
	srv := httptest.NewServer(http.StripPrefix("/images", b.Handler()))
	t.Cleanup(srv.Close)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(method, srv.URL+"/images/acc1/abc.png", nil)
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || err != nil {
			t.Fatalf("%s: %d %v", method, resp.StatusCode, resp.Header)
		}
		q := loc.Query()
		if loc.Scheme != "https" || !strings.HasPrefix(loc.Host, "mailgoose-test-images.s3.") || loc.Path != "/acc1/abc.png" ||
			q.Get("X-Amz-Expires") != "300" || q.Get("X-Amz-Signature") == "" {
			t.Errorf("%s: redirected to %s", method, loc)
		}
		if resp.Header.Get("Cache-Control") != "private, max-age=60" {
			t.Errorf("%s: Cache-Control %q", method, resp.Header.Get("Cache-Control"))
		}
	}

	for path, want := range map[string]int{"/images/": 404, "/images/acc1/": 404, "/images/../x.png": 404} {
		resp, err := noFollow.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	resp, err := noFollow.Post(srv.URL+"/images/acc1/abc.png", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d %v", resp.StatusCode, resp.Header)
	}
}
