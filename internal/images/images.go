// Package images stores the pictures used in image blocks (DES-5): JPEG,
// PNG, GIF and WebP up to 5 MB, checked by content, kept under the
// account's prefix with a random name and served from the CDN in front of
// the store.
package images

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxBytes is the largest image.
const MaxBytes = 5 << 20

// MaxPerAccount is how many images one account may keep, so the CDN isn't
// free file hosting (a reversible default; see docs/decisions.md).
const MaxPerAccount = 1000

// Kinds are the image types accepted, by content type, with their file
// extension.
var Kinds = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// Errors from Upload.
var (
	ErrEmpty    = errors.New("the image is empty")
	ErrTooLarge = fmt.Errorf("the image is over %d MB", MaxBytes>>20)
	ErrTooMany  = fmt.Errorf("the account already has %d images", MaxPerAccount)
)

// UnsupportedError means the file isn't one of the accepted kinds.
type UnsupportedError struct {
	// Detected is what the content looks like, such as "application/pdf".
	Detected string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("images must be JPEG, PNG, GIF or WebP; this looks like %s", e.Detected)
}

// Store keeps image files. Keys are "<account id>/<random>.<ext>".
type Store interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	// DeletePrefix removes every file whose key starts with prefix.
	DeletePrefix(ctx context.Context, prefix string) error
}

// Image is one uploaded picture.
type Image struct {
	ID          string
	URL         string
	FileName    string
	ContentType string
	Bytes       int
	CreatedAt   time.Time
}

// Service uploads and lists an account's images.
type Service struct {
	DB    *pgxpool.Pool
	Store Store
	// BaseURL is where the store's files are served from, such as the CDN's
	// https://img.mailgoose.app; an image's URL is BaseURL/key.
	BaseURL string
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) url(key string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/" + key
}

// Kind says what data is, by its content: the content type and extension of
// an accepted image, or an UnsupportedError.
func Kind(data []byte) (contentType, ext string, err error) {
	ct := http.DetectContentType(data)
	if ext, ok := Kinds[ct]; ok {
		return ct, ext, nil
	}
	// DetectContentType knows WebP only from Go 1.x on some versions; check
	// the RIFF header directly so it never depends on that.
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp", "webp", nil
	}
	return "", "", &UnsupportedError{Detected: strings.SplitN(ct, ";", 2)[0]}
}

// cleanName keeps a file name for showing, not for storing: the base name,
// valid UTF-8, at most 200 characters.
func cleanName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "." || name == "/" || !utf8.ValidString(name) {
		return ""
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	return name
}

// Upload checks data and stores it for the account.
func (s *Service) Upload(ctx context.Context, accountID, fileName string, data []byte) (*Image, error) {
	switch {
	case len(data) == 0:
		return nil, ErrEmpty
	case len(data) > MaxBytes:
		return nil, ErrTooLarge
	}
	ct, ext, err := Kind(data)
	if err != nil {
		return nil, err
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM images WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxPerAccount {
		return nil, ErrTooMany
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	key := accountID + "/" + hex.EncodeToString(b[:]) + "." + ext
	if err := s.Store.Put(ctx, key, ct, data); err != nil {
		return nil, fmt.Errorf("store image: %w", err)
	}
	img := &Image{URL: s.url(key), FileName: cleanName(fileName), ContentType: ct, Bytes: len(data), CreatedAt: s.now().UTC()}
	err = s.DB.QueryRow(ctx, `INSERT INTO images (account_id, key, file_name, content_type, bytes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, accountID, key, img.FileName, ct, len(data), img.CreatedAt).Scan(&img.ID)
	if err != nil {
		return nil, err
	}
	return img, nil
}

// Cursor is where a list page starts: after this image.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// List is the account's images, newest first, limit at a time, after the
// cursor if one is given.
func (s *Service) List(ctx context.Context, accountID string, after *Cursor, limit int) ([]Image, error) {
	q := `SELECT id, key, file_name, content_type, bytes, created_at FROM images WHERE account_id = $1`
	args := []any{accountID}
	if after != nil {
		q += ` AND (created_at, id) < ($2, $3)`
		args = append(args, after.CreatedAt, after.ID)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Image, error) {
		var img Image
		var key string
		err := r.Scan(&img.ID, &key, &img.FileName, &img.ContentType, &img.Bytes, &img.CreatedAt)
		img.URL = s.url(key)
		return img, err
	})
}

// DeleteAccount removes every file the account uploaded, for deleting the
// account; its rows go with the account's other data.
func (s *Service) DeleteAccount(ctx context.Context, accountID string) error {
	if accountID == "" || strings.ContainsAny(accountID, "/.") {
		return fmt.Errorf("images: bad account id %q", accountID)
	}
	return s.Store.DeletePrefix(ctx, accountID+"/")
}
