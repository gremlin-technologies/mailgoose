// Package brandassets serves the brand images that emails load (MG-119): the
// wordmark in the "Sent by mailgoose" footer of free accounts' campaigns. The
// API serves it at /assets/brand/, so no email depends on a third party's
// server, and the path carries a version so mail already sent keeps the
// image it was sent with.
package brandassets

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// Version is part of every image's path. Redrawing the wordmark means a new
// folder under files and a new Version, so sent mail and caches never see
// the image change under them.
const Version = "v1"

// Prefix is the path the API serves the images under.
const Prefix = "/assets/brand/"

//go:embed files
var files embed.FS

// WordmarkURL is where an email finds the wordmark, given the base URL the
// API is reached at (PUBLIC_BASE_URL). Empty base, empty URL.
func WordmarkURL(base string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + Prefix + Version + "/wordmark.png"
}

// Handler serves the images under Prefix, cached for a year: a path never
// changes what it holds.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "files")
	if err != nil {
		panic(err)
	}
	server := http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || !strings.HasSuffix(r.URL.Path, ".png") {
			http.NotFound(w, r) // no directory listings
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		server.ServeHTTP(w, r)
	})
}
