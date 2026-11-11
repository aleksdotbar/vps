package webassets

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

func newHandler(content fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(content, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded frontend index: %w", err)
	}
	if len(index) == 0 {
		return nil, errors.New("embedded frontend index is empty")
	}
	files := http.FileServerFS(content)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /assets/{file...}", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		info, err := fs.Stat(content, name)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
	indexHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
	for _, route := range []string{
		"/{$}", "/login", "/projects", "/projects/new", "/projects/{projectId}",
		"/projects/{projectId}/environments/new", "/servers", "/servers/{serverId}",
		"/environments/{environmentId}", "/environments/{environmentId}/settings",
		"/environments/{environmentId}/secrets", "/settings", "/settings/repositories",
		"/operations", "/operations/{operationId}", "/releases/{releaseId}",
	} {
		mux.Handle("GET "+route, indexHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Reject aliases and hidden files before ServeMux can redirect/clean them.
		if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") {
			http.NotFound(w, r)
			return
		}
		for _, part := range strings.Split(r.URL.Path, "/") {
			if strings.HasPrefix(part, ".") || (!strings.HasPrefix(r.URL.Path, "/assets/") && strings.Contains(part, ".")) {
				http.NotFound(w, r)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}
