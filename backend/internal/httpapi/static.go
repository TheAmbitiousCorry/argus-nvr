package httpapi

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"
)

func init() {
	// Go's table does not carry this one, and a manifest served as text/plain is
	// a manifest the browser ignores, which shows up as an app that simply
	// refuses to install with nothing to explain why.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// neverCached are the files that decide which version of everything else a
// browser is running. A service worker cached for a year is a service worker
// that cannot be replaced by deploying, and the manifest is what an installed
// app reads to find its own icons.
var neverCached = map[string]bool{
	"/sw.js":                true,
	"/manifest.webmanifest": true,
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// spaHandler serves the built frontend, falling back to index.html for paths
// that do not exist on disk so client-side routing survives a page reload.
func spaHandler(dir string) http.Handler {
	// http.Dir rejects paths that escape the directory, so traversal attempts
	// fail here rather than reaching the filesystem.
	root := http.Dir(dir)
	files := http.FileServer(root)
	index := filepath.Join(dir, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		if exists(root, clean) {
			if neverCached[clean] {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		if _, err := os.Stat(index); err != nil {
			http.NotFound(w, r)
			return
		}
		// The shell is the app itself, so caching it would pin users to an old
		// build after a deploy.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// exists reports whether the request maps to a real file, or to a directory
// that has its own index.html for the file server to render.
func exists(root http.Dir, name string) bool {
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	idx, err := root.Open(path.Join(name, "index.html"))
	if err != nil {
		return false
	}
	idx.Close()
	return true
}
