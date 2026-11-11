//go:build webassets

package webassets

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
)

// Only Vite's index and bundled assets belong in the released web root.
//
//go:embed dist/index.html dist/assets
var content embed.FS

func New() (http.Handler, error) {
	root, err := fs.Sub(content, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded frontend: %w", err)
	}
	return newHandler(root)
}
