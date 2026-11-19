package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/images"
)

// MaxImageBodyBytes is the largest upload request: a 5 MB image in base64
// is about 6.7 MB, plus room for the rest of the JSON.
const MaxImageBodyBytes = images.MaxBytes*4/3 + 64<<10

func (s *Server) imagesCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Images == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Image uploads need a database and an image store, and this server has none configured."})
		return nil
	}
	return c
}

// imageError writes the response for an images error, and reports whether
// there was one.
func (s *Server) imageError(w http.ResponseWriter, r *http.Request, err error) bool {
	var un *images.UnsupportedError
	switch {
	case err == nil:
		return false
	case errors.As(err, &un):
		WriteError(w, Unprocessable("invalid_image", "Images must be JPEG, PNG, GIF or WebP; this file looks like "+un.Detected+".").
			WithDetails(map[string]any{"detected": un.Detected}).
			WithHint("Save it as a PNG or JPEG and upload that."))
	case errors.Is(err, images.ErrTooLarge):
		WriteError(w, Unprocessable("invalid_image", "The image is over 5 MB.").
			WithHint("Resize or compress it; a picture in an email rarely needs to be wider than 1200 pixels."))
	case errors.Is(err, images.ErrTooMany):
		WriteError(w, Unprocessable("too_many_images", "The account already has "+strconv.Itoa(images.MaxPerAccount)+" images, the most it can keep.").
			WithHint("Reuse an uploaded image: `mailgoose image list` shows their URLs."))
	case errors.Is(err, images.ErrEmpty):
		WriteError(w, Unprocessable("invalid_image", "The image is empty."))
	default:
		s.Log.ErrorContext(r.Context(), "image request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The image couldn't be stored just now.", Hint: "Try again in a minute."})
	}
	return true
}

func imageJSON(img images.Image) apigen.Image {
	return apigen.Image{Id: img.ID, Url: img.URL, FileName: img.FileName, ContentType: apigen.ImageContentType(img.ContentType),
		Bytes: img.Bytes, CreatedAt: img.CreatedAt.UTC()}
}

func imageCursor(img images.Image) string {
	return img.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + img.ID
}

// UploadImage is POST /v1/images.
func (s *Server) UploadImage(w http.ResponseWriter, r *http.Request) {
	c := s.imagesCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.UploadImageRequest
	if err := decodeJSON(w, r, &req, MaxImageBodyBytes); err != nil {
		WriteError(w, err)
		return
	}
	img, err := s.Images.Upload(r.Context(), c.AccountID, deref(req.FileName), req.Data)
	if s.imageError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, imageJSON(*img))
}

// ListImages is GET /v1/images.
func (s *Server) ListImages(w http.ResponseWriter, r *http.Request, _ apigen.ListImagesParams) {
	c := s.imagesCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *images.Cursor
	if pr.After != "" {
		cur, ok := parseContactCursor(pr.After)
		if !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &images.Cursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	list, err := s.Images.List(r.Context(), c.AccountID, after, pr.Limit+1)
	if s.imageError(w, r, err) {
		return
	}
	page := NewPage(list, pr.Limit, imageCursor)
	out := apigen.ImageList{Data: make([]apigen.Image, len(page.Data)), NextCursor: page.NextCursor}
	for i, img := range page.Data {
		out.Data[i] = imageJSON(img)
	}
	writeJSON(w, http.StatusOK, out)
}
