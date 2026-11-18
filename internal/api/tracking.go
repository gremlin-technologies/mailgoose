package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/tracking"
)

// trackingPerMinute limits opens and clicks per IP. It's well above the
// other recipient pages' limit because mail providers fetch every
// recipient's pixel through a few image proxies.
const trackingPerMinute = 20000

// pixel is a transparent 1x1 GIF.
var pixel = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

var badTrackedLinkPage = page{
	Title: "This link doesn't work",
	Body: []string{
		"It may have been cut short or changed on the way. Open it straight from the email, or reply to the email and ask for the link.",
	},
}

var trackingUnavailablePage = page{
	Title: "This link isn't working right now",
	Body:  []string{"Please try it again in a few minutes."},
}

// isTrackingPath reports whether a path is an open pixel or tracked link.
func isTrackingPath(path string) bool {
	return strings.HasPrefix(path, tracking.OpenPrefix) || strings.HasPrefix(path, tracking.ClickPrefix)
}

// trackingRoutes serves the open pixel and tracked links (REP-3). Like the
// other recipient pages they're outside the spec and take no sign-in: the
// signed token is the only authority.
func (s *Server) trackingRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+tracking.OpenPrefix+"{token}", s.trackOpen)
	mux.HandleFunc("GET "+tracking.ClickPrefix+"{token}", s.trackClick)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, http.StatusBadRequest, badTrackedLinkPage)
	})
	return mux
}

// record stores a hit. It finishes even if the person's request is gone,
// and a failure is only logged: the redirect matters more than the count.
func (s *Server) record(ctx context.Context, h *tracking.Hit) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Tracking.Record(ctx, h); err != nil {
		s.Log.ErrorContext(ctx, "record a tracked hit", "err", err, "campaign_id", h.CampaignID)
	}
}

// trackOpen always answers with the pixel, whatever the token: a mail app
// shows a broken image otherwise. Only a valid token is recorded.
func (s *Server) trackOpen(w http.ResponseWriter, r *http.Request) {
	if s.Tracking != nil {
		h, err := s.Tracking.Check(r.Context(), r.PathValue("token"), false)
		switch {
		case err == nil:
			s.record(r.Context(), h)
		case !errors.Is(err, tracking.ErrBadToken):
			s.Log.ErrorContext(r.Context(), "check an open", "err", err)
		}
	}
	hd := w.Header()
	hd.Set("Content-Type", "image/gif")
	hd.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(pixel)
}

// trackClick records a click and sends the person on. A token that doesn't
// check out gets an error page, never a redirect, so the address can't be
// turned into an open redirect.
func (s *Server) trackClick(w http.ResponseWriter, r *http.Request) {
	if s.Tracking == nil {
		writePage(w, http.StatusServiceUnavailable, trackingUnavailablePage)
		return
	}
	h, err := s.Tracking.Check(r.Context(), r.PathValue("token"), true)
	switch {
	case errors.Is(err, tracking.ErrBadToken):
		writePage(w, http.StatusBadRequest, badTrackedLinkPage)
		return
	case err != nil:
		s.Log.ErrorContext(r.Context(), "check a click", "err", err)
		writePage(w, http.StatusServiceUnavailable, trackingUnavailablePage)
		return
	}
	s.record(r.Context(), h)
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, h.URL, http.StatusFound) //nolint:gosec // h.URL is from a token checked against the account's signature, never the raw request
}
