// Package socialicons holds the social block's icons (MG-109): a PNG per
// network in each color, drawn from Font Awesome Free's brand glyphs
// (CC BY 4.0; see README.md). The API serves them at /assets/social/, and
// the renderer points emails there, so no email depends on a third party's
// server for its icons.
package socialicons

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Version is part of every icon's path. Redrawing the icons means a new
// version (a new folder under icons), so emails already sent keep the icons
// they were sent with and caches never serve a stale one.
const Version = "v1"

// Prefix is the path the API serves the icons under.
const Prefix = "/assets/social/"

//go:embed icons
var files embed.FS

// URL is where an email finds a network's icon in a color, given the base
// URL the API is reached at (PUBLIC_BASE_URL). Empty base, empty URL.
func URL(base, color, network string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + Prefix + Version + "/" + color + "/" + network + ".png"
}

// Has reports whether there's an icon for network in color.
func Has(color, network string) bool {
	_, err := fs.Stat(files, path.Join("icons", Version, color, network+".png"))
	return err == nil
}

// Handler serves the icons under Prefix, cached for a year: a path never
// changes what it holds.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "icons")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || !strings.HasSuffix(r.URL.Path, ".png") {
			http.NotFound(w, r) // no directory listings
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		files.ServeHTTP(w, r)
	})
}
