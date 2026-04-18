// Package web exposes the embedded static web UI.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// Handler returns a handler that serves the embedded UI assets, with
// SPA-style fallback to index.html for unknown paths.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pass /api/* through (it's only here defensively; the API mux
		// already matches those routes).
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		// If the file does not exist, fall back to index.html.
		if _, err := fs.Stat(sub, trimLeadingSlash(path)); err != nil {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
