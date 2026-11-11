package githubflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var ErrArtifact = errors.New("build artifact is missing or incompatible")

const maxArchiveBytes = 1 << 20

// ArtifactURL admits only the public HTTPS origins used by native Actions storage.
// The transport separately checks each resolved address before connecting.
func ArtifactURL(value *url.URL) bool {
	if value == nil || value.Scheme != "https" || value.User != nil || value.Port() != "" || value.Fragment != "" {
		return false
	}
	host := strings.ToLower(value.Hostname())
	return (strings.HasSuffix(host, ".blob.core.windows.net") && len(strings.TrimSuffix(host, ".blob.core.windows.net")) > 0) || (strings.HasSuffix(host, ".actions.githubusercontent.com") && len(strings.TrimSuffix(host, ".actions.githubusercontent.com")) > 0)
}

// NewArtifactTransport has no credentials, proxy or private-network fallback.
// The caller must still validate the signed URL and deny HTTP redirects.
func NewArtifactTransport() http.RoundTripper {
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, ForceAttemptHTTP2: true}
	transport.Proxy = nil
	// ControlContext receives each numeric IP chosen by net.Dialer, retaining
	// native IPv4/IPv6 fallback while rejecting private targets before connect.
	dialer := &net.Dialer{Timeout: 5 * time.Second, ControlContext: func(_ context.Context, _ string, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrArtifact
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !publicAddress(ip) {
			return ErrArtifact
		}
		return nil
	}}
	transport.DialContext = dialer.DialContext

	return transport
}
func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	// CDN IPv6 must use ordinary global-unicast allocation, excluding translated,
	// local-use and protocol-special prefixes from the IANA registry.
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
func decodeArchive(data []byte, digest string) ([]byte, error) {
	if len(data) > maxArchiveBytes || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
		return nil, ErrArtifact
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		return nil, ErrArtifact
	}
	hash := sha256.Sum256(data)
	if !bytes.Equal(hash[:], expected) {
		return nil, ErrArtifact
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 {
		return nil, ErrArtifact
	}
	file := reader.File[0]
	if file.Name != "release.json" || !file.Mode().IsRegular() || file.UncompressedSize64 > releases.MaxArtifactBytes {
		return nil, ErrArtifact
	}
	stream, err := file.Open()
	if err != nil {
		return nil, ErrArtifact
	}
	defer func() { _ = stream.Close() }()
	value, err := io.ReadAll(io.LimitReader(stream, releases.MaxArtifactBytes+1))
	if err != nil || len(value) > releases.MaxArtifactBytes {
		return nil, ErrArtifact
	}
	return value, nil
}
func (b *Builds) artifact(ctx context.Context, client *github.Client, repository store.GithubRepository, run *github.WorkflowRun, requestID string) ([]byte, error) {
	name := "panel-release-" + formatID(run.GetID()) + "-" + formatID(int64(run.GetRunAttempt()))
	list, response, err := client.Actions.ListWorkflowRunArtifacts(ctx, repository.Owner, repository.Name, run.GetID(), &github.ListOptions{PerPage: 100, Page: 1})
	if err != nil || list == nil {
		return nil, b.integration.failure(ctx, response, err)
	}
	if response.NextPage != 0 || list.GetTotalCount() > 100 {
		return nil, ErrArtifact
	}
	var selected *github.Artifact
	for _, artifact := range list.Artifacts {
		if artifact.GetName() == name {
			if selected != nil {
				return nil, ErrArtifact
			}
			selected = artifact
		}
	}
	if selected == nil || selected.GetID() < 1 || selected.GetExpired() || selected.GetSizeInBytes() < 1 || selected.GetSizeInBytes() > maxArchiveBytes || selected.GetDigest() == "" {
		return nil, ErrArtifact
	}
	identity := selected.WorkflowRun
	if identity == nil || identity.GetID() != run.GetID() || identity.GetRepositoryID() != repository.ID || identity.GetHeadRepositoryID() != repository.ID || identity.GetHeadSHA() != run.GetHeadSHA() || identity.GetHeadBranch() != run.GetHeadBranch() {
		return nil, ErrArtifact
	}
	bound, err := store.New(b.integration.database).BindWaitingArtifact(ctx, store.BindWaitingArtifactParams{ID: requestID, RunID: run.GetID(), RunAttempt: int64(run.GetRunAttempt()), ArtifactID: selected.GetID(), ArtifactDigest: selected.GetDigest()})
	if err != nil {
		return nil, err
	}
	if bound != 1 {
		return nil, ErrArtifact
	}
	location, response, err := client.Actions.DownloadArtifact(ctx, repository.Owner, repository.Name, selected.GetID(), 0)
	if err != nil {
		return nil, b.integration.failure(ctx, response, err)
	}
	if !ArtifactURL(location) {
		return nil, ErrArtifact
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, ErrArtifact
	}
	// Separate unauthenticated client: App/installation/PAT credentials never
	// accompany signed artifact URLs, and signed URL queries are never logged.
	download := &http.Client{Transport: b.integration.artifactTransport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	result, err := download.Do(request)
	if err != nil {
		return nil, &Failure{}
	}
	defer func() { _ = result.Body.Close() }()
	if result.StatusCode != http.StatusOK {
		return nil, &Failure{}
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, &Failure{}
	}
	return decodeArchive(data, selected.GetDigest())
}
func (b *Builds) source(ctx context.Context, client *github.Client, repository store.GithubRepository, selection projects.Selection, sha string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "panel-compose-source-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	root, err := os.OpenRoot(directory)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	for _, path := range []string{"compose.yaml", selection.Environment.OverridePath} {
		file, entries, response, err := client.Repositories.GetContents(ctx, repository.Owner, repository.Name, path, &github.RepositoryContentGetOptions{Ref: sha})
		if err != nil {
			cleanup()
			return "", nil, b.integration.failure(ctx, response, err)
		}
		if file == nil || len(entries) > 0 || file.GetType() != "file" || file.GetSubmoduleGitURL() != "" || file.GetTarget() != "" || file.GetSize() < 0 || file.GetSize() > 256<<10 {
			cleanup()
			return "", nil, ErrArtifact
		}
		value, err := file.GetContent()
		if err != nil || len(value) > 256<<10 {
			cleanup()
			return "", nil, ErrArtifact
		}
		if err := root.MkdirAll(filepath.Dir(path), 0700); err != nil {
			cleanup()
			return "", nil, ErrArtifact
		}
		if err := root.WriteFile(path, []byte(value), 0600); err != nil {
			cleanup()
			return "", nil, ErrArtifact
		}
	}
	return directory, cleanup, nil
}
