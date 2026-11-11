package githubflow

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v93/github"
	githubauth "github.com/jferrl/go-githubauth"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/mutations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type Service struct {
	database          *sql.DB
	cipher            *securestate.Cipher
	vault             *securestate.Vault
	transport         http.RoundTripper
	artifactTransport http.RoundTripper
	providerPermits   chan struct{}
	tokenMu           sync.Mutex
	tokens            map[tokenScope]installationToken
	cooldownUntil     time.Time
	origin            string
	mutations         *mutations.Service
}
type Browser struct {
	SessionHash []byte
	Generation  int64
}

func New(database *sql.DB, cipher *securestate.Cipher, origin string, transport http.RoundTripper) *Service {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Service{database: database, cipher: cipher, vault: securestate.NewVault(database, cipher), transport: transport, artifactTransport: NewArtifactTransport(), providerPermits: make(chan struct{}, 4), tokens: make(map[tokenScope]installationToken), origin: origin, mutations: mutations.New(database, cipher)}
}
func decodeCredential(value []byte) (appCredential, error) {
	var result appCredential
	if json.Unmarshal(value, &result) != nil || result.ClientID == "" || result.PrivateKey == "" {
		return result, ErrSetup
	}
	return result, nil
}
func stateToken() (string, []byte, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(random)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}
func (s *Service) insertState(ctx context.Context, queries *store.Queries, phase string, ownerID int64, owner string, browser Browser) (string, error) {
	if len(browser.SessionHash) != 32 || browser.Generation < 1 {
		return "", ErrState
	}
	if err := queries.PruneGitHubSetupStates(ctx, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	count, err := queries.GitHubSetupStateCount(ctx)
	if err != nil {
		return "", err
	}
	if count >= 32 {
		return "", mutations.ErrCapacity
	}
	administrator, err := queries.GetAdministrator(ctx)
	if err != nil || administrator.Generation != browser.Generation {
		return "", ErrState
	}
	token, hash, err := stateToken()
	if err != nil {
		return "", err
	}
	err = queries.CreateGitHubSetupState(ctx, store.CreateGitHubSetupStateParams{StateHash: hash, Phase: phase, OwnerID: ownerID, OwnerLogin: owner, SessionHash: browser.SessionHash, Generation: browser.Generation, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli()})
	return token, err
}
func (s *Service) consumeState(ctx context.Context, token, phase string, browser Browser) (store.GithubSetupState, error) {
	if len(token) != 64 || len(browser.SessionHash) != 32 {
		return store.GithubSetupState{}, ErrState
	}
	if _, err := hex.DecodeString(token); err != nil {
		return store.GithubSetupState{}, ErrState
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return store.GithubSetupState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	administrator, err := store.New(tx).GetAdministrator(ctx)
	if err != nil || administrator.Generation != browser.Generation {
		return store.GithubSetupState{}, ErrState
	}
	state, err := store.New(tx).GetGitHubSetupState(ctx, hash[:])
	if err != nil {
		return state, ErrState
	}
	if state.Phase != phase || state.Generation != browser.Generation || !hmac.Equal(state.SessionHash, browser.SessionHash) {
		return state, ErrState
	}
	changed, err := store.New(tx).ConsumeGitHubSetupState(ctx, store.ConsumeGitHubSetupStateParams{StateHash: hash[:], ExpiresAt: time.Now().UnixMilli()})
	if err != nil {
		return state, err
	}
	if changed != 1 {
		return state, ErrState
	}
	if err := tx.Commit(); err != nil {
		return state, err
	}
	return state, nil
}
func (s *Service) BeginSetup(ctx context.Context, request *panelv1.BeginSetupRequest, browser Browser) (*panelv1.BeginSetupResponse, error) {
	if len(browser.SessionHash) != 32 || browser.Generation < 1 {
		return nil, ErrState
	}
	procedure := fmt.Sprintf("panel.v1.GitHubService/BeginSetup/%x/%d", browser.SessionHash[:16], browser.Generation)
	value, err := s.mutations.Begin(ctx, procedure, request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	if value.Response != nil {
		defer func() { _ = value.Tx.Rollback() }()
		response := new(panelv1.BeginSetupResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrState
		}
		return response, nil
	}
	if err := value.Tx.Rollback(); err != nil {
		return nil, err
	}
	if _, err := store.New(s.database).GetGitHubIntegration(ctx); err == nil {
		return nil, ErrSetup
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	client, err := s.client("")
	if err != nil {
		return nil, err
	}
	var ownerID int64
	var login string
	if request.GetOrganization() {
		owner, response, err := client.Organizations.Get(ctx, request.GetOwner())
		if err != nil || owner == nil {
			return nil, s.failure(ctx, response, err)
		}
		ownerID = owner.GetID()
		login = owner.GetLogin()
	} else {
		owner, response, err := client.Users.Get(ctx, request.GetOwner())
		if err != nil || owner == nil {
			return nil, s.failure(ctx, response, err)
		}
		ownerID = owner.GetID()
		login = owner.GetLogin()
	}
	if ownerID < 1 || !strings.EqualFold(login, request.GetOwner()) {
		return nil, ErrOwner
	}
	value, err = s.mutations.Begin(ctx, procedure, request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.BeginSetupResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrState
		}
		return response, nil
	}
	token, err := s.insertState(ctx, store.New(value.Tx), "manifest", ownerID, login, browser)
	if err != nil {
		return nil, err
	}
	manifest, err := json.Marshal(map[string]any{"name": request.GetName(), "url": s.origin, "redirect_url": s.origin + "/github/manifest", "setup_url": s.origin + "/github/installation", "public": false, "request_oauth_on_install": false, "setup_on_update": true, "hook_attributes": map[string]any{"active": false, "url": s.origin + "/github/webhook-disabled"}, "default_permissions": map[string]string{"contents": "read", "actions": "write"}, "default_events": []string{}})
	if err != nil {
		return nil, err
	}
	action := "https://github.com/settings/apps/new"
	if request.GetOrganization() {
		action = "https://github.com/organizations/" + url.PathEscape(login) + "/settings/apps/new"
	}
	response := &panelv1.BeginSetupResponse{ActionUrl: action + "?state=" + url.QueryEscape(token), ManifestJson: string(manifest)}
	if err := s.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func (s *Service) CompleteManifest(ctx context.Context, token, code string, browser Browser) (string, error) {
	state, err := s.consumeState(ctx, token, "manifest", browser)
	if err != nil {
		return "", err
	}
	if len(code) < 1 || len(code) > 256 || strings.ContainsAny(code, "/?#%\\\x00") {
		return "", ErrInvalid
	}
	client, err := s.client("")
	if err != nil {
		return "", err
	}
	configuration, response, err := client.Apps.CompleteAppManifest(ctx, code)
	if err != nil || configuration == nil {
		return "", s.failure(ctx, response, err)
	}
	if configuration.Owner.GetID() != state.OwnerID || !strings.EqualFold(configuration.Owner.GetLogin(), state.OwnerLogin) || configuration.GetID() < 1 || configuration.GetSlug() == "" || configuration.GetClientID() == "" {
		return "", ErrOwner
	}
	// Only credentials needed for App JWTs are retained. OAuth/webhook secrets
	// from manifest conversion are deliberately discarded, never logged/returned.
	credential, err := json.Marshal(appCredential{ClientID: configuration.GetClientID(), PrivateKey: configuration.GetPEM()})
	if err != nil {
		return "", ErrSetup
	}
	if _, err := githubauth.NewApplicationTokenSource(configuration.GetClientID(), []byte(configuration.GetPEM())); err != nil {
		return "", err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	ref, err := s.vault.SaveCredentialTx(ctx, tx, "github-app", 0, credential)
	if err != nil {
		return "", err
	}
	if err := store.New(tx).CreateGitHubIntegration(ctx, store.CreateGitHubIntegrationParams{AppID: configuration.GetID(), OwnerID: state.OwnerID, OwnerLogin: state.OwnerLogin, AppSlug: configuration.GetSlug(), CredentialVersion: ref.Version}); err != nil {
		return "", err
	}
	next, err := s.insertState(ctx, store.New(tx), "installation", state.OwnerID, state.OwnerLogin, browser)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "https://github.com/apps/" + url.PathEscape(configuration.GetSlug()) + "/installations/new?state=" + url.QueryEscape(next), nil
}
func (s *Service) CompleteInstallation(ctx context.Context, token string, installationID int64, browser Browser) error {
	state, err := s.consumeState(ctx, token, "installation", browser)
	if err != nil {
		return err
	}
	if installationID < 1 {
		return ErrInvalid
	}
	client, integration, err := s.appClient(ctx)
	if err != nil {
		return err
	}
	installation, response, err := client.Apps.GetInstallation(ctx, installationID)
	if err != nil || installation == nil {
		return s.failure(ctx, response, err)
	}
	if (integration.InstallationID != 0 && integration.InstallationID != installationID) || integration.OwnerID != state.OwnerID || installation.GetAppID() != integration.AppID || installation.Account.GetID() != state.OwnerID || installation.GetRepositorySelection() != "selected" || installation.SuspendedAt != nil {
		return ErrOwner
	}
	permissionData, _ := json.Marshal(installation.Permissions)
	var permissions map[string]string
	if json.Unmarshal(permissionData, &permissions) != nil {
		return ErrOwner
	}
	for name, value := range permissions {
		if (name == "contents" && value == "read") || (name == "actions" && value == "write") || (name == "metadata" && value == "read") {
			continue
		}
		return ErrOwner
	}
	if permissions["contents"] != "read" || permissions["actions"] != "write" {
		return ErrOwner
	}
	readPermission := "read"
	tokenResult, response, err := client.Apps.CreateInstallationToken(ctx, installationID, &github.InstallationTokenOptions{Permissions: &github.InstallationPermissions{Contents: &readPermission, Actions: &readPermission}})
	if err != nil || tokenResult == nil {
		return s.failure(ctx, response, err)
	}
	installed, err := s.client(tokenResult.GetToken())
	if err != nil {
		return err
	}
	repositories, response, err := installed.Apps.ListRepos(ctx, &github.ListOptions{PerPage: 100, Page: 1})
	if err != nil || repositories == nil {
		return s.failure(ctx, response, err)
	}
	if response.NextPage != 0 || repositories.GetTotalCount() > 100 {
		return mutations.ErrCapacity
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	administrator, err := store.New(tx).GetAdministrator(ctx)
	if err != nil || administrator.Generation != browser.Generation {
		return ErrState
	}
	previous, err := store.New(tx).ListGitHubRepositories(ctx, store.ListGitHubRepositoriesParams{ID: 0, Limit: 100})
	if err != nil {
		return err
	}
	selection := make(map[int64]bool, len(previous))
	for _, repository := range previous {
		selection[repository.ID] = repository.Selected
	}
	if err := store.New(tx).ClearGitHubRepositories(ctx); err != nil {
		return err
	}
	for _, repository := range repositories.Repositories {
		if repository.GetID() < 1 || repository.Owner.GetID() != state.OwnerID {
			return ErrOwner
		}
		if err := store.New(tx).CreateGitHubRepository(ctx, store.CreateGitHubRepositoryParams{ID: repository.GetID(), Owner: repository.Owner.GetLogin(), Name: repository.GetName()}); err != nil {
			return err
		}
		if selected, exists := selection[repository.GetID()]; exists && !selected {
			// Preserve local deselection when GitHub still grants repository access.
			if err := store.New(tx).ClearSelectedGitHubRepository(ctx, repository.GetID()); err != nil {
				return err
			}
		}
		if err := store.New(tx).UpdateProjectRepositoryName(ctx, store.UpdateProjectRepositoryNameParams{RepositoryOwner: repository.Owner.GetLogin(), RepositoryName: repository.GetName(), RepositoryID: repository.GetID()}); err != nil {
			return err
		}

	}
	if err := store.New(tx).SetGitHubInstallation(ctx, installationID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) GetIntegration(ctx context.Context) (*panelv1.GetIntegrationResponse, error) {
	result := new(panelv1.GetIntegrationResponse)
	integration, err := store.New(s.database).GetGitHubIntegration(ctx)
	if err == nil {
		result.Registered = true
		result.Installed = integration.InstallationID > 0
		result.AppId = integration.AppID
		result.OwnerId = integration.OwnerID
		result.Owner = integration.OwnerLogin
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	registry, err := store.New(s.database).GetRegistryCredential(ctx)
	if err == nil {
		if registry.Version < 1 {
			return nil, ErrSetup
		}
		result.RegistryCredentialConfigured = true
		result.RegistryUsername = registry.Username
		result.RegistryVersion = uint64(registry.Version)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return result, nil
}
func (s *Service) SaveRegistryCredential(ctx context.Context, request *panelv1.SaveRegistryCredentialRequest) (*panelv1.SaveRegistryCredentialResponse, error) {
	version := request.GetExpectedVersion()
	if version > math.MaxInt64 {
		return nil, ErrInvalid
	}
	// Classic PATs only; GHCR package access/read:packages is checked at pull time.
	if !strings.HasPrefix(request.GetToken(), "ghp_") || strings.ContainsAny(request.GetToken(), "\r\n\x00") {
		return nil, ErrInvalid
	}
	value, err := s.mutations.Begin(ctx, "panel.v1.GitHubService/SaveRegistryCredential", request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.SaveRegistryCredentialResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrSetup
		}
		return response, nil
	}
	ref, err := s.vault.SaveCredentialTx(ctx, value.Tx, "ghcr-read", int64(version), []byte(request.GetToken()))
	if err != nil {
		return nil, err
	}
	if err := store.New(value.Tx).SetRegistryCredential(ctx, store.SetRegistryCredentialParams{Username: request.GetUsername(), Version: ref.Version}); err != nil {
		return nil, err
	}
	if ref.Version < 1 {
		return nil, ErrSetup
	}
	response := &panelv1.SaveRegistryCredentialResponse{Version: uint64(ref.Version)}
	if err := s.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func repositoryMessage(value store.GithubRepository) *panelv1.Repository {
	return &panelv1.Repository{GithubRepositoryId: value.ID, Owner: value.Owner, Name: value.Name}
}
func providerPage(request *panelv1.PageRequest, scope string) (int, int, error) {
	size := request.GetPageSize()
	if size == 0 {
		size = 50
	}
	if size > 100 {
		return 0, 0, ErrInvalid
	}
	page := 1
	if request.GetPageToken() != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(request.GetPageToken())
		if err != nil || !strings.HasPrefix(string(decoded), scope+":") {
			return 0, 0, ErrInvalid
		}
		value, err := strconv.Atoi(strings.TrimPrefix(string(decoded), scope+":"))
		if err != nil || value < 1 || value > 100 {
			return 0, 0, ErrInvalid
		}
		page = value
	}
	return page, int(size), nil
}
func (s *Service) ListRepositories(ctx context.Context, request *panelv1.PageRequest) (*panelv1.ListRepositoriesResponse, error) {
	size := request.GetPageSize()
	if size == 0 {
		size = 50
	}
	if size > 100 {
		return nil, ErrInvalid
	}
	after := int64(0)
	if request.GetPageToken() != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(request.GetPageToken())
		if err != nil || !strings.HasPrefix(string(decoded), "repositories:") {
			return nil, ErrInvalid
		}
		after, err = strconv.ParseInt(strings.TrimPrefix(string(decoded), "repositories:"), 10, 64)
		if err != nil || after < 1 {
			return nil, ErrInvalid
		}
	}
	repositories, err := store.New(s.database).ListGitHubRepositories(ctx, store.ListGitHubRepositoriesParams{ID: after, Limit: int64(size) + 1})
	if err != nil {
		return nil, err
	}
	result := &panelv1.ListRepositoriesResponse{Page: &panelv1.PageResponse{}}
	if len(repositories) > int(size) {
		result.Page.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte("repositories:" + strconv.FormatInt(repositories[size-1].ID, 10)))
		repositories = repositories[:size]
	}
	for _, repository := range repositories {
		result.Repositories = append(result.Repositories, repositoryMessage(repository))
		if repository.Selected {
			result.SelectedRepositoryIds = append(result.SelectedRepositoryIds, repository.ID)
		}
	}
	return result, nil
}
func (s *Service) SelectRepositories(ctx context.Context, request *panelv1.SelectRepositoriesRequest) (*panelv1.SelectRepositoriesResponse, error) {
	canonical := proto.Clone(request).(*panelv1.SelectRepositoriesRequest)
	sort.Slice(canonical.RepositoryIds, func(i, j int) bool { return canonical.RepositoryIds[i] < canonical.RepositoryIds[j] })
	value, err := s.mutations.Begin(ctx, "panel.v1.GitHubService/SelectRepositories", canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.SelectRepositoriesResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrSetup
		}
		return response, nil
	}
	if err := store.New(value.Tx).ClearGitHubRepositorySelection(ctx); err != nil {
		return nil, err
	}
	response := new(panelv1.SelectRepositoriesResponse)
	for _, id := range canonical.RepositoryIds {
		repository, err := store.New(value.Tx).GetGitHubRepository(ctx, id)
		if err != nil {
			return nil, ErrOwner
		}
		if err := store.New(value.Tx).SelectGitHubRepository(ctx, id); err != nil {
			return nil, err
		}
		response.Repositories = append(response.Repositories, repositoryMessage(repository))
	}
	if err := s.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) BeginInstallation(ctx context.Context, request *panelv1.BeginInstallationRequest, browser Browser) (*panelv1.BeginInstallationResponse, error) {
	if len(browser.SessionHash) != 32 || browser.Generation < 1 {
		return nil, ErrState
	}
	procedure := fmt.Sprintf("panel.v1.GitHubService/BeginInstallation/%x/%d", browser.SessionHash[:16], browser.Generation)
	value, err := s.mutations.Begin(ctx, procedure, request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.BeginInstallationResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrState
		}
		return response, nil
	}
	integration, err := store.New(value.Tx).GetGitHubIntegration(ctx)
	if err != nil {
		return nil, ErrSetup
	}

	token, err := s.insertState(ctx, store.New(value.Tx), "installation", integration.OwnerID, integration.OwnerLogin, browser)
	if err != nil {
		return nil, err
	}
	response := &panelv1.BeginInstallationResponse{InstallationUrl: "https://github.com/apps/" + url.PathEscape(integration.AppSlug) + "/installations/new?state=" + url.QueryEscape(token)}
	if err := s.mutations.Commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
