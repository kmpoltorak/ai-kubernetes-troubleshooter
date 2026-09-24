package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// ui/dist is produced by `make web` (Vite build of ./web). The ui directory
// always exists so the Go build works even when the frontend is not built.
//
//go:embed all:ui
var uiFS embed.FS

const uiMissing = `<!doctype html><title>Kubernetes Troubleshooter</title>
<p>The web UI is not built. Run <code>make web</code> and rebuild, or use the REST API under /api/v1.</p>`

// registerUI serves the embedded single-page app. It talks only to the public
// REST API, so it adds no server-side behavior.
func registerUI(mux *http.ServeMux) {
	dist, _ := fs.Sub(uiFS, "ui/dist") // Sub only fails on invalid paths
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(uiMissing))
		})
		return
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, dist, "index.html")
	})
	mux.Handle("GET /assets/", http.FileServerFS(dist))
}
