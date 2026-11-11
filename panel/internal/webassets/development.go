//go:build !webassets

package webassets

import "net/http"

// Backend-only builds support clean-checkout checks and the separate Vite server.
func New() (http.Handler, error) {
	return http.NotFoundHandler(), nil
}
