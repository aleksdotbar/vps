//go:build webassets

package webassets

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseServesRealBundledEntrypointAndRejectsBuildMetadata(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/settings", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `<div id="root"></div>`) || strings.Contains(body, "/src/main.tsx") {
		t.Fatalf("release did not serve production entrypoint: status=%d", response.Code)
	}
	assets := regexp.MustCompile(`(?:src|href)="(/assets/[^\"]+)"`).FindAllStringSubmatch(body, -1)
	if len(assets) < 2 {
		t.Fatal("production entrypoint lacks its bundled script and stylesheet")
	}
	for _, asset := range assets {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, asset[1], nil))
		if response.Code != http.StatusOK || response.Body.Len() == 0 || strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("production asset %q: status=%d type=%q", asset[1], response.Code, response.Header().Get("Content-Type"))
		}
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.vite/manifest.json", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("build metadata exposed: %d", response.Code)
	}
}
