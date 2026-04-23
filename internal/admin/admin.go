// Package admin serves the embedded React admin SPA.
package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded admin SPA.
// All requests that don't match a static file fall back to dist/index.html
// so React Router can handle client-side routing.
func Handler() http.Handler {
	// Strip the 'dist' prefix from the embed paths.
	content, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("admin: failed to open dist subdir: " + err.Error())
	}
	fsys := http.FS(content)
	fileServer := http.FileServer(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the path is a file, serve it directly.
		p := path.Clean(r.URL.Path)
		if p == "/" {
			p = "/index.html"
		}
		f, err := fsys.Open(p)
		if err == nil {
			stat, _ := f.Stat()
			f.Close()
			if !stat.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Otherwise fall back to index.html for client-side routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
