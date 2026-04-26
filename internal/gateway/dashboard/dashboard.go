// Package dashboard serves the embedded HTML+JS frontend for conduit's
// in-binary dashboards (today: the brain memory-graph viewer at /dashboard/brain).
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

// AssetsFS returns a filesystem rooted at the assets/ directory so handlers
// can serve files like brain.html and vis-network.min.js by their bare names.
func AssetsFS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("dashboard: embedded assets/ subtree missing: " + err.Error())
	}
	return sub
}

// AssetsHandler serves the contents of assets/ via http.FileServer.
// Use it under a path prefix (e.g. /dashboard/assets/) with http.StripPrefix.
func AssetsHandler() http.Handler {
	return http.FileServer(http.FS(AssetsFS()))
}

// BrainHandler serves the brain.html page at any matched path.
func BrainHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(AssetsFS(), "brain.html")
		if err != nil {
			http.Error(w, "dashboard asset missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
}
