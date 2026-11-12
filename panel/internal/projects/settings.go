package projects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

func (s *Service) CreateProject(ctx context.Context, request *panelv1.CreateProjectRequest) (*panelv1.CreateProjectResponse, error) {
	if !ownerPattern.MatchString(request.GetRepository().GetOwner()) || !repositoryPattern.MatchString(request.GetRepository().GetName()) {
		return nil, ErrInvalid
	}
	value, err := s.begin(ctx, "CreateProject", request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.CreateProjectResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrInvalid
		}
		return response, nil
	}
	capacity, err := store.New(value.Tx).MetadataCapacity(ctx)
	if err != nil {
		return nil, err
	}
	if capacity.Projects >= 128 {
		return nil, ErrCapacity
	}
	id := uuid.NewString()
	if err := store.New(value.Tx).CreateProject(ctx, store.CreateProjectParams{ID: id, Name: request.GetName(), RepositoryID: request.GetRepository().GetGithubRepositoryId(), RepositoryOwner: request.GetRepository().GetOwner(), RepositoryName: request.GetRepository().GetName(), CreatedAt: time.Now().UnixMilli()}); err != nil {
		return nil, writeError(err)
	}
	project, err := store.New(value.Tx).GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	response := &panelv1.CreateProjectResponse{Project: projectMessage(project)}
	if err := s.commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func canonicalSettings(settings *panelv1.EnvironmentSettings) (*panelv1.EnvironmentSettings, error) {
	if settings == nil {
		return nil, ErrInvalid
	}
	result := proto.Clone(settings).(*panelv1.EnvironmentSettings)
	server, err := uuid.Parse(result.GetServerId())
	if err != nil {
		return nil, ErrInvalid
	}
	result.ServerId = server.String()
	branch := result.GetBranch()
	path := result.GetComposeOverridePath()
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return nil, ErrInvalid
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasSuffix(part, ".lock") || strings.HasPrefix(part, ".") {
			return nil, ErrInvalid
		}
	}
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\\\x00") || path == "compose.yaml" || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
		return nil, ErrInvalid
	}
	sort.Slice(result.Domains, func(i, j int) bool { return result.Domains[i].GetHostname() < result.Domains[j].GetHostname() })
	for index, domain := range result.Domains {
		if index > 0 && domain.GetHostname() == result.Domains[index-1].GetHostname() {
			return nil, ErrInvalid
		}
	}
	return result, nil
}
func (s *Service) CreateEnvironment(ctx context.Context, request *panelv1.CreateEnvironmentRequest) (*panelv1.CreateEnvironmentResponse, error) {
	settings, err := canonicalSettings(request.GetSettings())
	if err != nil {
		return nil, err
	}
	canonical := proto.Clone(request).(*panelv1.CreateEnvironmentRequest)
	canonical.Settings = settings
	projectID, err := uuid.Parse(request.GetProjectId())
	if err != nil {
		return nil, ErrInvalid
	}
	canonical.ProjectId = projectID.String()
	value, err := s.begin(ctx, "CreateEnvironment", canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.CreateEnvironmentResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrInvalid
		}
		return response, nil
	}
	queries := store.New(value.Tx)
	if _, err := queries.GetProject(ctx, canonical.ProjectId); err != nil {
		return nil, readError(err)
	}
	if _, err := queries.GetServer(ctx, settings.ServerId); err != nil {
		return nil, readError(err)
	}
	capacity, err := queries.MetadataCapacity(ctx)
	if err != nil {
		return nil, err
	}
	if capacity.Environments >= 512 {
		return nil, ErrCapacity
	}
	id := uuid.NewString()
	sum := sha256.Sum256([]byte(id))
	bridge := "vps-" + hex.EncodeToString(sum[:5])
	if _, err := s.vault.UpdateEnvironmentTx(ctx, value.Tx, id, 0, nil, nil); err != nil {
		return nil, err
	}
	domains, _ := json.Marshal(settings.Domains)
	if err := queries.CreateEnvironment(ctx, store.CreateEnvironmentParams{ID: id, ProjectID: canonical.ProjectId, Name: settings.Name, ServerID: settings.ServerId, Branch: settings.Branch, OverridePath: settings.ComposeOverridePath, DomainsJson: domains, AutoDeploy: settings.AutoDeploy, SecretNamesJson: []byte("[]"), BridgeName: bridge, CreatedAt: time.Now().UnixMilli()}); err != nil {
		return nil, writeError(err)
	}
	if err := reserveDomains(ctx, queries, id, settings.Domains); err != nil {
		return nil, err
	}
	environment, err := queries.GetEnvironment(ctx, id)
	if err != nil {
		return nil, err
	}
	message, err := environmentMessage(environment)
	if err != nil {
		return nil, err
	}
	response := &panelv1.CreateEnvironmentResponse{Environment: message}
	if err := s.commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func (s *Service) UpdateEnvironment(ctx context.Context, request *panelv1.UpdateEnvironmentRequest) (*panelv1.UpdateEnvironmentResponse, error) {
	settings, err := canonicalSettings(request.GetSettings())
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(request.GetId())
	if err != nil {
		return nil, ErrInvalid
	}
	expected, err := storeVersion(request.GetExpectedVersion())
	if err != nil {
		return nil, err
	}
	canonical := proto.Clone(request).(*panelv1.UpdateEnvironmentRequest)
	canonical.Id = id.String()
	canonical.Settings = settings
	value, err := s.begin(ctx, "UpdateEnvironment", canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.UpdateEnvironmentResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrInvalid
		}
		return response, nil
	}
	queries := store.New(value.Tx)
	previous, err := queries.GetEnvironment(ctx, id.String())
	if err != nil {
		return nil, readError(err)
	}
	if previous.Version != expected {
		return nil, ErrStale
	}
	if _, err := queries.GetServer(ctx, settings.ServerId); err != nil {
		return nil, readError(err)
	}
	binding := previous.BindingVersion
	changedBinding := previous.ServerID != settings.ServerId || previous.Branch != settings.Branch || previous.OverridePath != settings.ComposeOverridePath
	if previous.ServerID != settings.ServerId {
		started, err := queries.StartedEnvironmentOperationCount(ctx, previous.ID)
		if err != nil {
			return nil, err
		}
		if previous.CurrentReleaseID.Valid || previous.PreviousReleaseID.Valid || started > 0 {
			return nil, ErrBinding
		}
	}
	if changedBinding {
		binding++
	}
	domains, _ := json.Marshal(settings.Domains)
	if err := queries.UpdateEnvironment(ctx, store.UpdateEnvironmentParams{Name: settings.Name, ServerID: settings.ServerId, Branch: settings.Branch, OverridePath: settings.ComposeOverridePath, DomainsJson: domains, AutoDeploy: settings.AutoDeploy, BindingVersion: binding, ID: previous.ID}); err != nil {
		return nil, writeError(err)
	}
	if err := reserveDomains(ctx, queries, previous.ID, settings.Domains); err != nil {
		return nil, err
	}
	if changedBinding {
		if err := queries.SupersedeStaleWaitingBuilds(ctx, store.SupersedeStaleWaitingBuildsParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: previous.ID, BindingVersion: binding}); err != nil {
			return nil, err
		}
		if err := queries.SupersedeOldBindingOperations(ctx, store.SupersedeOldBindingOperationsParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: previous.ID, BindingVersion: binding}); err != nil {
			return nil, err
		}
	}
	if previous.AutoDeploy && !settings.AutoDeploy {
		if err := queries.SupersedeDisabledWaitingBuilds(ctx, store.SupersedeDisabledWaitingBuildsParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: previous.ID}); err != nil {
			return nil, err
		}
		if err := queries.SupersedeQueuedAutomaticOperations(ctx, store.SupersedeQueuedAutomaticOperationsParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: previous.ID}); err != nil {
			return nil, err
		}
	}
	environment, err := queries.GetEnvironment(ctx, previous.ID)
	if err != nil {
		return nil, err
	}
	message, err := environmentMessage(environment)
	if err != nil {
		return nil, err
	}
	response := &panelv1.UpdateEnvironmentResponse{Environment: message}
	if err := s.commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func (s *Service) SaveSecrets(ctx context.Context, request *panelv1.SaveEnvironmentSecretsRequest) (*panelv1.SaveEnvironmentSecretsResponse, error) {
	id, err := uuid.Parse(request.GetEnvironmentId())
	if err != nil {
		return nil, ErrInvalid
	}
	expected, err := storeVersion(request.GetExpectedVersion())
	if err != nil {
		return nil, err
	}
	canonical := proto.Clone(request).(*panelv1.SaveEnvironmentSecretsRequest)
	canonical.EnvironmentId = id.String()
	sort.Slice(canonical.Upserts, func(i, j int) bool { return canonical.Upserts[i].GetName() < canonical.Upserts[j].GetName() })
	sort.Strings(canonical.RemoveNames)
	upserts := make(map[string]string, len(canonical.Upserts))
	for _, item := range canonical.Upserts {
		// The panel supplies GIT_SHA to Compose; a secret of that name would
		// make every release unresolvable.
		if _, exists := upserts[item.GetName()]; exists || item.GetName() == compose.SourceVariable {
			return nil, ErrInvalid
		}
		upserts[item.GetName()] = item.GetValue()
	}
	value, err := s.begin(ctx, "SaveEnvironmentSecrets", canonical.GetMutation().GetRequestId(), canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response != nil {
		response := new(panelv1.SaveEnvironmentSecretsResponse)
		if proto.Unmarshal(value.Response, response) != nil {
			return nil, ErrInvalid
		}
		return response, nil
	}
	queries := store.New(value.Tx)
	previous, err := queries.GetEnvironment(ctx, id.String())
	if err != nil {
		return nil, readError(err)
	}
	if previous.Version != expected {
		return nil, ErrStale
	}
	ref, err := s.vault.UpdateEnvironmentTx(ctx, value.Tx, previous.ID, previous.SecretVersion, upserts, canonical.RemoveNames)
	if err != nil {
		if errors.Is(err, securestate.ErrInvalidInput) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	values, err := s.vault.EnvironmentTx(ctx, value.Tx, ref)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	encoded, _ := json.Marshal(names)
	if err := queries.UpdateEnvironmentSecrets(ctx, store.UpdateEnvironmentSecretsParams{SecretVersion: ref.Version, SecretNamesJson: encoded, ID: previous.ID}); err != nil {
		return nil, err
	}
	environment, err := queries.GetEnvironment(ctx, previous.ID)
	if err != nil {
		return nil, err
	}
	message, err := environmentMessage(environment)
	if err != nil {
		return nil, err
	}
	response := &panelv1.SaveEnvironmentSecretsResponse{Environment: message}
	if err := s.commit(ctx, value, response); err != nil {
		return nil, err
	}
	return response, nil
}
func reserveDomains(ctx context.Context, queries *store.Queries, id string, domains []*panelv1.Domain) error {
	environment, err := queries.GetEnvironment(ctx, id)
	if err != nil {
		return err
	}
	if err := routes.CheckDesiredOwnership(ctx, queries, id, environment.ServerID, domains); err != nil {
		return err
	}
	if err := queries.DeleteDesiredDomains(ctx, id); err != nil {
		return err
	}
	for _, domain := range domains {
		if err := queries.ReserveDesiredDomain(ctx, store.ReserveDesiredDomainParams{Hostname: domain.GetHostname(), EnvironmentID: id}); err != nil {
			return writeError(err)
		}
	}
	return nil
}
func readError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func writeError(err error) error {
	var databaseError *sqlite.Error
	if errors.As(err, &databaseError) && (databaseError.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || databaseError.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return ErrConflict
	}
	return err
}

// ReplayProjectCreation lets the authenticated provider boundary replay durable
// local success before making any GitHub request. The transaction is read-only
// in effect and always closed before provider IO.
func (s *Service) ReplayProjectCreation(ctx context.Context, request *panelv1.CreateProjectRequest) (*panelv1.CreateProjectResponse, bool, error) {
	value, err := s.begin(ctx, "CreateProject", request.GetMutation().GetRequestId(), request)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = value.Tx.Rollback() }()
	if value.Response == nil {
		return nil, false, nil
	}
	response := new(panelv1.CreateProjectResponse)
	if proto.Unmarshal(value.Response, response) != nil {
		return nil, false, errCorruptMetadata
	}
	return response, true, nil
}
