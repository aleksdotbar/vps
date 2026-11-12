package githubflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-github/v93/github"
	githubauth "github.com/jferrl/go-githubauth"

	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var (
	ErrBusy     = errors.New("GitHub integration is busy")
	ErrInvalid  = errors.New("invalid GitHub integration input")
	ErrSetup    = errors.New("GitHub integration requires setup")
	ErrOwner    = errors.New("GitHub owner or installation does not match")
	ErrProvider = errors.New("GitHub request could not be completed")
	ErrState    = errors.New("GitHub setup state is invalid or expired")
)

type appCredential struct {
	ClientID   string `json:"client_id"`
	PrivateKey string `json:"private_key"`
}

// Requests use the fixed public GitHub API only. Tests replace the underlying
// transport, never install a user-controlled provider endpoint. No raw SDK error
// escapes this package: manifest codes and signed URLs can occur in its text.
type apiTransport struct {
	base    http.RoundTripper
	permits chan struct{}
	service *Service
}

func (t apiTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if delay := t.service.providerDelay(); delay > 0 {
		return nil, &Failure{After: delay, RateLimited: true}
	}
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" {
		return nil, ErrProvider
	}
	select {
	case t.permits <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	clone := request.Clone(request.Context())
	clone.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		<-t.permits
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		return nil, ErrProvider
	}
	if response.StatusCode == http.StatusUnauthorized {
		t.service.clearTokens()
	}
	if response.StatusCode == http.StatusTooManyRequests || response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0" {
		t.service.clearTokensOnFailure(ProviderFailure(request.Context(), &github.Response{Response: response}, nil))
	}
	response.Body = &limitedBody{Reader: io.LimitReader(response.Body, 2<<20), closer: response.Body, release: func() { <-t.permits }}
	return response, nil
}

type limitedBody struct {
	io.Reader
	closer  io.Closer
	release func()
	once    sync.Once
}

func (b *limitedBody) Close() error { err := b.closer.Close(); b.once.Do(b.release); return err }
func (s *Service) client(token string) (*github.Client, error) {
	options := []github.ClientOptionsFunc{github.WithRateLimitRedirectionalEndpoints(), github.WithHTTPClient(&http.Client{Transport: apiTransport{s.transport, s.providerPermits, s}, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})}
	if token != "" {
		options = append(options, github.WithAuthToken(token))
	}
	client, err := github.NewClient(options...)
	if err != nil {
		return nil, ErrProvider
	}
	return client, nil
}
func (s *Service) appClient(ctx context.Context) (*github.Client, store.GithubIntegration, error) {
	integration, err := store.New(s.database).GetGitHubIntegration(ctx)
	if err != nil {
		return nil, integration, ErrSetup
	}
	value, err := s.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "github-app", Version: integration.CredentialVersion})
	if err != nil {
		return nil, integration, err
	}
	credential, err := decodeCredential(value)
	if err != nil {
		return nil, integration, err
	}
	source, err := githubauth.NewApplicationTokenSource(credential.ClientID, []byte(credential.PrivateKey))
	if err != nil {
		return nil, integration, ErrSetup
	}
	token, err := source.Token()
	if err != nil {
		return nil, integration, ErrProvider
	}
	client, err := s.client(token.AccessToken)
	return client, integration, err
}

type tokenScope struct {
	RepositoryID, InstallationID, CredentialVersion int64
}
type installationToken struct {
	value   string
	expires time.Time
}

func (s *Service) clearTokens() { s.tokenMu.Lock(); clear(s.tokens); s.tokenMu.Unlock() }
func (s *Service) installationClient(ctx context.Context, repositoryIDs []int64) (*github.Client, error) {
	if len(repositoryIDs) != 1 || repositoryIDs[0] < 1 {
		return nil, ErrInvalid
	}
	integration, err := store.New(s.database).GetGitHubIntegration(ctx)
	if err != nil || integration.InstallationID < 1 {
		return nil, ErrSetup
	}
	scope := tokenScope{RepositoryID: repositoryIDs[0], InstallationID: integration.InstallationID, CredentialVersion: integration.CredentialVersion}
	s.tokenMu.Lock()
	cached, found := s.tokens[scope]
	s.tokenMu.Unlock()
	if found && cached.expires.After(time.Now().Add(time.Minute)) {
		return s.client(cached.value)
	}
	app, _, err := s.appClient(ctx)
	if err != nil {
		return nil, err
	}
	readPermission := "read"
	token, response, err := app.Apps.CreateInstallationToken(ctx, integration.InstallationID, &github.InstallationTokenOptions{RepositoryIDs: repositoryIDs, Permissions: &github.InstallationPermissions{Contents: &readPermission}})
	if err != nil || token == nil || token.GetToken() == "" {
		return nil, s.failure(ctx, response, err)
	}
	if token.ExpiresAt != nil && token.ExpiresAt.After(time.Now().Add(time.Minute)) && token.ExpiresAt.Before(time.Now().Add(61*time.Minute)) {
		s.tokenMu.Lock()
		if len(s.tokens) >= 200 {
			clear(s.tokens)
		}
		s.tokens[scope] = installationToken{value: token.GetToken(), expires: token.ExpiresAt.Time}
		s.tokenMu.Unlock()
	}
	return s.client(token.GetToken())
}
