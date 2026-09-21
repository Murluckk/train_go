// Package web serves the single page front end, embedded in the binary so
// that the trainer is one file to build and one file to run.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:static
var staticFS embed.FS

// Handler serves the front end. Everything that is not an asset returns the
// shell, so that deep links like /#/task/slices-01 survive a reload.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(sub)

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			serveIndex(w, index)
			return
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			serveIndex(w, index)
			return
		}
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is rebuilt with the binary, so caching it only causes
	// confusion after a rebuild.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}
