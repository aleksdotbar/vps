//go:build webassets

package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
)

func getEmbedded(t *testing.T, i *installation, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, i.origin+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := i.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestEmbeddedUILeavesRPCAuthenticationAndSecurityHeadersIntact(t *testing.T) {
	i := setup(t)
	response := getEmbedded(t, i, "/projects")
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="root"`) || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "form-action 'self' https://github.com") {
		t.Fatalf("embedded navigation boundary: status=%d headers=%v", response.StatusCode, response.Header)
	}
	client := panelv1connect.NewPanelServiceClient(i.client, i.origin)
	_, err = client.ListProjects(context.Background(), connect.NewRequest(&panelv1.ListProjectsRequest{Page: &panelv1.PageRequest{PageSize: 1}}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("embedded UI bypassed RPC authentication: %v", err)
	}
	for _, path := range []string{"/panel.v1.UnknownService/Call", "/assets/missing.js", "/.vite/manifest.json"} {
		response = getEmbedded(t, i, path)
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read rejected path response: %v %v", readErr, closeErr)
		}
		if response.StatusCode != http.StatusNotFound || strings.Contains(response.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("RPC or asset %q reached SPA: status=%d type=%q", path, response.StatusCode, response.Header.Get("Content-Type"))
		}
	}
}

func TestEmbeddedAssetsDoNotRequireSessionStorage(t *testing.T) {
	i := setup(t)
	if _, err := login(i, "initial-private-password", i.origin); err != nil {
		t.Fatal(err)
	}
	if err := i.database.Close(); err != nil {
		t.Fatal(err)
	}
	response := getEmbedded(t, i, "/settings")
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read static page response: %v %v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("embedded page used unavailable session storage: %d", response.StatusCode)
	}
}
