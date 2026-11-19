package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Files served from the store are cached for a year: a key never changes
// what it holds.
const cacheControl = "public, max-age=31536000, immutable"

// Without a CDN, the API redirects each image request to a presigned URL
// that lasts this long, and lets the redirect be cached for a minute. The
// URL in an email is the API's, so it never expires.
const (
	presignFor    = 5 * time.Minute
	redirectCache = "private, max-age=60"
)

// S3 is a Store in an S3 bucket, normally with a CDN in front of it;
// without one, Handler serves it.
type S3 struct {
	Client *s3.Client
	Bucket string
	// Log records presigning failures; nil uses slog's default.
	Log *slog.Logger
}

// Handler serves the bucket's files when there's no CDN, mounted at
// /images/: each GET or HEAD is redirected to a short-lived presigned URL,
// so the bucket stays private and the API sends no image bytes itself.
func (s *S3) Handler() http.Handler {
	presign := s3.NewPresignClient(s.Client, s3.WithPresignExpires(presignFor))
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		if !validKey(key) || strings.HasSuffix(key, "/") {
			http.NotFound(w, r)
			return
		}
		var (
			req *v4.PresignedHTTPRequest
			err error
		)
		switch r.Method {
		case http.MethodGet:
			req, err = presign.PresignGetObject(r.Context(), &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
		case http.MethodHead:
			req, err = presign.PresignHeadObject(r.Context(), &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			log.ErrorContext(r.Context(), "presign image", "key", key, "error", err)
			http.Error(w, "the image can't be served right now", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", redirectCache)
		http.Redirect(w, r, req.URL, http.StatusFound)
	})
}

// validKey reports whether key is one the API could have stored: not
// empty, not absolute, and with no "..".
func validKey(key string) bool {
	return key != "" && !strings.Contains(key, "..") && !strings.HasPrefix(key, "/")
}

// Put stores one file.
func (s *S3) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), CacheControl: aws.String(cacheControl),
		ContentLength: aws.Int64(int64(len(body))),
	})
	return err
}

// DeletePrefix removes every file under prefix, a page of 1,000 at a time.
func (s *S3) DeletePrefix(ctx context.Context, prefix string) error {
	pages := s3.NewListObjectsV2Paginator(s.Client, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		if len(page.Contents) == 0 {
			continue
		}
		ids := make([]s3types.ObjectIdentifier, len(page.Contents))
		for i, o := range page.Contents {
			ids[i] = s3types.ObjectIdentifier{Key: o.Key}
		}
		out, err := s.Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(s.Bucket),
			Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
		if err != nil {
			return err
		}
		if len(out.Errors) > 0 {
			return fmt.Errorf("delete %s: %s", aws.ToString(out.Errors[0].Key), aws.ToString(out.Errors[0].Message))
		}
	}
	return nil
}

// Dir is a Store in a local directory, for running locally; Handler serves
// it.
type Dir struct {
	Path string
}

func (d *Dir) file(key string) (string, error) {
	if !validKey(key) {
		return "", fmt.Errorf("images: bad key %q", key)
	}
	return filepath.Join(d.Path, filepath.FromSlash(key)), nil
}

// Put stores one file.
func (d *Dir) Put(_ context.Context, key, _ string, body []byte) error {
	f, err := d.file(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
		return err
	}
	return os.WriteFile(f, body, 0o600)
}

// DeletePrefix removes the account directory a prefix names.
func (d *Dir) DeletePrefix(_ context.Context, prefix string) error {
	f, err := d.file(strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(f); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Handler serves the directory's files, without listings.
func (d *Dir) Handler() http.Handler {
	files := http.FileServer(http.Dir(d.Path))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", cacheControl)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
}

// Memory is a Store for tests.
type Memory struct {
	mu    sync.Mutex
	Files map[string][]byte
	Err   error
}

// Put stores one file.
func (m *Memory) Put(_ context.Context, key, _ string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if m.Files == nil {
		m.Files = map[string][]byte{}
	}
	m.Files[key] = append([]byte(nil), body...)
	return nil
}

// DeletePrefix removes every file under prefix.
func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	for k := range m.Files {
		if strings.HasPrefix(k, prefix) {
			delete(m.Files, k)
		}
	}
	return nil
}

// Keys lists the stored keys, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
