package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func sum(data []byte) string {
	value := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(value[:])
}

// fakeRegistry serves GHCR's anonymous token, manifest and blob endpoints.
func fakeRegistry(t *testing.T, manifests map[string][]byte, mediaTypes map[string]string, blobs map[string][]byte) *Resolver {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/token":
			if request.URL.Query().Get("scope") != "repository:owner/app:pull" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write([]byte(`{"token":"anonymous"}`))
		case strings.HasPrefix(request.URL.Path, "/v2/owner/app/manifests/"):
			if request.Header.Get("Authorization") != "Bearer anonymous" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			tag := strings.TrimPrefix(request.URL.Path, "/v2/owner/app/manifests/")
			body, exists := manifests[tag]
			if !exists {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Content-Type", mediaTypes[tag])
			writer.Header().Set("Docker-Content-Digest", sum(body))
			_, _ = writer.Write(body)
		case strings.HasPrefix(request.URL.Path, "/v2/owner/app/blobs/"):
			body, exists := blobs[strings.TrimPrefix(request.URL.Path, "/v2/owner/app/blobs/")]
			if !exists {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write(body)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return &Resolver{client: server.Client(), baseURL: server.URL}
}

func TestResolveManifestListRequiresLinuxAmd64(t *testing.T) {
	index := func(architecture string) []byte {
		data, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaOCIIndex, "manifests": []any{map[string]any{"digest": "sha256:" + strings.Repeat("a", 64), "platform": map[string]string{"architecture": architecture, "os": "linux"}}}})
		return data
	}
	amd, arm := index("amd64"), index("arm64")
	resolver := fakeRegistry(t, map[string][]byte{"amd": amd, "arm": arm}, map[string]string{"amd": mediaOCIIndex, "arm": mediaOCIIndex}, nil)
	digest, err := resolver.Resolve(context.Background(), "ghcr.io/owner/app", "amd")
	if err != nil || digest != sum(amd) {
		t.Fatalf("index digest not returned: %q %v", digest, err)
	}
	if _, err := resolver.Resolve(context.Background(), "ghcr.io/owner/app", "arm"); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("arm-only index accepted: %v", err)
	}
}

func TestResolveSingleManifestChecksConfigPlatform(t *testing.T) {
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	other := []byte(`{"architecture":"arm64","os":"linux"}`)
	manifest := func(blob []byte) []byte {
		data, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaDockerV2, "config": map[string]any{"digest": sum(blob), "size": len(blob)}})
		return data
	}
	good, bad := manifest(config), manifest(other)
	resolver := fakeRegistry(t, map[string][]byte{"good": good, "bad": bad}, map[string]string{"good": mediaDockerV2, "bad": mediaDockerV2}, map[string][]byte{sum(config): config, sum(other): other})
	digest, err := resolver.Resolve(context.Background(), "ghcr.io/owner/app", "good")
	if err != nil || digest != sum(good) {
		t.Fatalf("manifest digest not returned: %q %v", digest, err)
	}
	if _, err := resolver.Resolve(context.Background(), "ghcr.io/owner/app", "bad"); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("non-amd64 image accepted: %v", err)
	}
}

func TestResolveMissingOrPrivateTagIsNotFound(t *testing.T) {
	resolver := fakeRegistry(t, nil, nil, nil)
	if _, err := resolver.Resolve(context.Background(), "ghcr.io/owner/app", strings.Repeat("0", 40)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing tag: %v", err)
	}
	if _, err := resolver.Resolve(context.Background(), "ghcr.io/owner/private", "tag"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private repository: %v", err)
	}
}

func TestResolveRejectsInvalidReferencesBeforeNetwork(t *testing.T) {
	resolver := &Resolver{client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid reference reached the network")
		return nil, nil
	})}, baseURL: "https://ghcr.invalid"}
	for _, test := range []struct{ repository, tag string }{
		{"docker.io/owner/app", "tag"},
		{"ghcr.io/Owner/app", "tag"},
		{"ghcr.io/app", "tag"},
		{"ghcr.io/owner/app", "../tag"},
		{"ghcr.io/owner/app", ""},
	} {
		if _, err := resolver.Resolve(context.Background(), test.repository, test.tag); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s:%s accepted: %v", test.repository, test.tag, err)
		}
	}
}

func TestPublicAddressRejectsPrivateAndSpecialRanges(t *testing.T) {
	for _, test := range []struct {
		value   string
		allowed bool
	}{{"140.82.112.33", true}, {"2606:50c0:8000::154", true}, {"127.0.0.1", false}, {"10.0.0.1", false}, {"100.64.1.2", false}, {"169.254.169.254", false}, {"::ffff:127.0.0.1", false}, {"fc00::1", false}, {"fe80::1", false}, {"64:ff9b::a00:1", false}} {
		if got := PublicAddress(netip.MustParseAddr(test.value)); got != test.allowed {
			t.Fatalf("public address %s=%t", test.value, got)
		}
	}
}

func TestBlobRedirectStaysOnGitHubAndDropsToken(t *testing.T) {
	via := []*http.Request{{}}
	allowed, _ := http.NewRequest(http.MethodGet, "https://pkg-containers.githubusercontent.com/blob", nil)
	allowed.Header.Set("Authorization", "Bearer anonymous")
	if err := blobRedirect(allowed, via); err != nil || allowed.Header.Get("Authorization") != "" {
		t.Fatalf("CDN redirect refused or kept token: %v", err)
	}
	for _, target := range []string{"http://pkg-containers.githubusercontent.com/blob", "https://evil.example/blob", "https://githubusercontent.com.evil.example/blob"} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if blobRedirect(request, via) == nil {
			t.Fatalf("redirect to %s allowed", target)
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
