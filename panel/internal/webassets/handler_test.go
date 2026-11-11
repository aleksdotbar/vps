package webassets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const fixtureIndex = "<!doctype html><div id=\"root\"></div><script type=\"module\" src=\"/assets/app-12345678.js\"></script>"

func fixtureHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := newHandler(fstest.MapFS{
		"index.html":              {Data: []byte(fixtureIndex)},
		"assets/app-12345678.js":  {Data: []byte("export const fixture = true;\n")},
		"assets/app-12345678.css": {Data: []byte("body { color: black; }\n")},
		"assets/.private.js":      {Data: []byte("not public")},
		"assets/nested/file.js":   {Data: []byte("not a listing")},
		".vite/manifest.json":     {Data: []byte("not public")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestSPAFallbackIsLimitedToApplicationNavigation(t *testing.T) {
	handler := fixtureHandler(t)
	for _, path := range []string{
		"/", "/login", "/projects", "/projects/new", "/projects/fixture", "/projects/fixture/environments/new",
		"/servers", "/servers/fixture", "/environments/fixture", "/environments/fixture/settings",
		"/environments/fixture/secrets", "/settings", "/settings/repositories",
		"/operations", "/operations/fixture", "/releases/fixture",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != fixtureIndex || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("navigation %q: status=%d cache=%q body=%q", path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
	for _, path := range []string{
		"/missing.js", "/projects/missing.js", "/assets/missing.js", "/assets/nested", "/assets/nested/",
		"/assets/.private.js", "/.vite/manifest.json", "/.git/config", "/index.html",
		"/panel.v1.PanelService/Unknown", "/panel.v1.UnknownService/Call", "/github/unknown", "/healthz",
		"/assets/../settings", "/assets/%2e%2e/settings", "/projects//fixture", "/projects/fixture/unknown",
		"/operations/missing.js", "/releases/fixture/unknown",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "root") || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("non-navigation %q reached SPA or cache: status=%d cache=%q", path, response.Code, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestBundledAssetsAndHTTPMethodsKeepSeparateContracts(t *testing.T) {
	handler := fixtureHandler(t)
	for _, item := range []struct{ path, media string }{
		{"/assets/app-12345678.js", "javascript"},
		{"/assets/app-12345678.css", "text/css"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, item.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), item.media) || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || response.Body.String() == fixtureIndex {
			t.Fatalf("asset %q: status=%d type=%q cache=%q", item.path, response.Code, response.Header().Get("Content-Type"), response.Header().Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/", "/settings", "/assets/app-12345678.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader("not a mutation")))
		if response.Code != http.StatusMethodNotAllowed || strings.Contains(response.Body.String(), "root") {
			t.Fatalf("POST %q served SPA: %d", path, response.Code)
		}
	}
}
