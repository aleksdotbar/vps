// Package registry resolves public GHCR image tags to immutable digests.
// Requests are anonymous, bounded and limited to public network addresses.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrNotFound covers missing tags and repositories that are not public;
	// GHCR does not distinguish them for anonymous clients.
	ErrNotFound = errors.New("image tag was not found or is not public")
	// ErrIncompatible means the image exists but cannot run on linux/amd64.
	ErrIncompatible = errors.New("image is not a linux/amd64 image")
	// ErrUnavailable is a transport or registry failure that may succeed later.
	ErrUnavailable = errors.New("image registry is unavailable")
)

const (
	host          = "ghcr.io"
	maxDocument   = 256 << 10
	maxTokenBytes = 16 << 10
)

// Repository is a lowercase GHCR repository path such as ghcr.io/owner/name.
var Repository = regexp.MustCompile(`^ghcr\.io/[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)+$`)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

const (
	mediaOCIIndex      = "application/vnd.oci.image.index.v1+json"
	mediaDockerList    = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIManifest   = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerV2      = "application/vnd.docker.distribution.manifest.v2+json"
	acceptedManifests  = mediaOCIIndex + ", " + mediaDockerList + ", " + mediaOCIManifest + ", " + mediaDockerV2
	platformArchitect  = "amd64"
	platformOS         = "linux"
	maxIndexManifests  = 64
	requestTimeout     = 15 * time.Second
	maxRedirectsToBlob = 1
)

// Resolver resolves tags through the GHCR distribution API.
type Resolver struct {
	client  *http.Client
	baseURL string
}

// New returns a resolver whose transport never uses proxies or credentials.
func New() *Resolver {
	return &Resolver{client: &http.Client{Transport: Transport(), Timeout: requestTimeout, CheckRedirect: blobRedirect}, baseURL: "https://" + host}
}

// Transport has no proxy and connects only to public unicast addresses.
func Transport() http.RoundTripper {
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, ForceAttemptHTTP2: true}
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 5 * time.Second, ControlContext: func(_ context.Context, _ string, address string, _ syscall.RawConn) error {
		value, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrUnavailable
		}
		ip, err := netip.ParseAddr(value)
		if err != nil || !PublicAddress(ip) {
			return ErrUnavailable
		}
		return nil
	}}
	transport.DialContext = dialer.DialContext
	return transport
}

// PublicAddress rejects private, local, translated and documentation ranges.
func PublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, value := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(value).Contains(address) {
			return false
		}
	}
	return true
}

// GHCR serves config blobs from its package CDN. Redirects must stay on HTTPS
// GitHub-operated hosts and never carry the registry token.
func blobRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > maxRedirectsToBlob || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Port() != "" {
		return http.ErrUseLastResponse
	}
	name := strings.ToLower(request.URL.Hostname())
	if name != host && !strings.HasSuffix(name, ".githubusercontent.com") {
		return http.ErrUseLastResponse
	}
	if name != host {
		request.Header.Del("Authorization")
	}
	return nil
}

// Resolve returns the immutable digest for repository:tag. For a manifest list
// it returns the list digest after confirming a linux/amd64 entry exists.
func (r *Resolver) Resolve(ctx context.Context, repository, tag string) (string, error) {
	if !Repository.MatchString(repository) || len(repository) > 256 || !tagPattern.MatchString(tag) {
		return "", ErrNotFound
	}
	name := strings.TrimPrefix(repository, host+"/")
	token, err := r.token(ctx, name)
	if err != nil {
		return "", err
	}
	body, mediaType, digest, err := r.manifest(ctx, name, tag, token)
	if err != nil {
		return "", err
	}
	switch mediaType {
	case mediaOCIIndex, mediaDockerList:
		if !indexHasPlatform(body) {
			return "", ErrIncompatible
		}
	case mediaOCIManifest, mediaDockerV2:
		config, err := manifestConfig(body)
		if err != nil {
			return "", err
		}
		data, err := r.get(ctx, r.baseURL+"/v2/"+name+"/blobs/"+config, token, "")
		if err != nil {
			return "", err
		}
		if !configPlatform(data.body, config) {
			return "", ErrIncompatible
		}
	default:
		return "", ErrIncompatible
	}
	return digest, nil
}

func (r *Resolver) token(ctx context.Context, name string) (string, error) {
	query := url.Values{"scope": {"repository:" + name + ":pull"}, "service": {host}}
	response, err := r.get(ctx, r.baseURL+"/token?"+query.Encode(), "", "")
	if err != nil {
		return "", err
	}
	var value struct {
		Token string `json:"token"`
	}
	if len(response.body) > maxTokenBytes || json.Unmarshal(response.body, &value) != nil || value.Token == "" || len(value.Token) > 8<<10 {
		return "", ErrUnavailable
	}
	return value.Token, nil
}

func (r *Resolver) manifest(ctx context.Context, name, reference, token string) ([]byte, string, string, error) {
	response, err := r.get(ctx, r.baseURL+"/v2/"+name+"/manifests/"+reference, token, acceptedManifests)
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(response.body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	// The registry's digest header must name exactly the bytes received.
	if header := response.header.Get("Docker-Content-Digest"); header != "" && header != digest {
		return nil, "", "", ErrUnavailable
	}
	mediaType := strings.TrimSpace(strings.Split(response.header.Get("Content-Type"), ";")[0])
	var declared struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(response.body, &declared) != nil {
		return nil, "", "", ErrIncompatible
	}
	if declared.MediaType != "" {
		mediaType = declared.MediaType
	}
	return response.body, mediaType, digest, nil
}

type fetched struct {
	body   []byte
	header http.Header
}

func (r *Resolver) get(ctx context.Context, target, token, accept string) (fetched, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fetched{}, ErrNotFound
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := r.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fetched{}, ctx.Err()
		}
		return fetched{}, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return fetched{}, ErrNotFound
	default:
		return fetched{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDocument+1))
	if err != nil {
		return fetched{}, ErrUnavailable
	}
	if len(body) > maxDocument {
		return fetched{}, ErrIncompatible
	}
	return fetched{body: body, header: response.Header}, nil
}

func indexHasPlatform(body []byte) bool {
	var index struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &index) != nil || len(index.Manifests) == 0 || len(index.Manifests) > maxIndexManifests {
		return false
	}
	for _, entry := range index.Manifests {
		if digestPattern.MatchString(entry.Digest) && entry.Platform.Architecture == platformArchitect && entry.Platform.OS == platformOS {
			return true
		}
	}
	return false
}

func manifestConfig(body []byte) (string, error) {
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &manifest) != nil || !digestPattern.MatchString(manifest.Config.Digest) || manifest.Config.Size < 1 || manifest.Config.Size > maxDocument {
		return "", ErrIncompatible
	}
	return manifest.Config.Digest, nil
}

func configPlatform(body []byte, digest string) bool {
	sum := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return false
	}
	var config struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	}
	return json.Unmarshal(body, &config) == nil && config.Architecture == platformArchitect && config.OS == platformOS
}
