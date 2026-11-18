// Package web serves the built web app (web/dist) from the API's own origin.
//
// Files under assets/ have content hashes in their names, so they're cached
// for a year. Any other path without a file extension gets index.html, and the
// app's router takes it from there; a missing file with an extension is a 404.
package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// contentSecurityPolicy keeps the app to its own scripts, and keeps it out of
// frames so nobody can trick a click on the CLI approval page.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; connect-src 'self'; object-src 'none'; base-uri 'self'; " +
	"form-action 'self'; frame-ancestors 'none'"

// New returns a handler for the app in fsys, which must hold index.html.
func New(fsys fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, fmt.Errorf("web app: %w (build it with `make web`)", err)
	}
	return &handler{fsys: fsys, index: index}, nil
}

type handler struct {
	fsys  fs.FS
	index []byte
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if hidden(name) {
		http.NotFound(w, r)
		return
	}
	info, err := fs.Stat(h.fsys, name)
	switch {
	case err == nil && info.Mode().IsRegular():
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// name is cleaned above, and an fs.FS refuses paths that leave it.
		http.ServeFileFS(w, r, h.fsys, name) //nolint:gosec // see above
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
	case path.Ext(name) != "":
		// A file the app links to but that isn't there (an old asset after a
		// deploy, say). index.html in its place would only confuse the browser.
		http.NotFound(w, r)
	default:
		h.serveIndex(w, r)
	}
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Content-Security-Policy", contentSecurityPolicy)
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.index)
	}
}

// hidden reports whether any part of the path starts with a dot.
func hidden(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
