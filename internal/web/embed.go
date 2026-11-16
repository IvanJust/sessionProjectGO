package web

import (
	"embed"
	"net/http"
)

//go:embed index.html app.css app.js
var assets embed.FS

func Handler() http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/", "/app.css", "/app.js":
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
