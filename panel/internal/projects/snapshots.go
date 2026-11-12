package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/readiness"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// Selection is nonsecret store data captured at accepted intent time. P05 may
// persist it for waiting builds; never populate it from remote/client payloads.
// Secret/domain-only edits after capture do not alter this selection.
type Selection struct {
	Environment store.Environment `json:"environment"`
}

// Build identifies the source commit; Artifact holds the image digests the
// caller resolved from that commit's GIT_SHA tags.
type Build struct {
	RepositoryID      int64
	Branch, SourceSHA string
}
type ReleaseInput struct {
	Intent    operations.Intent
	Selection Selection
	Build     Build
	Tree      string
	Artifact  releases.Artifact
}

// CaptureSelection composes with P05's accepted waiting-build intent transaction.
func (s *Service) CaptureSelection(ctx context.Context, tx *sql.Tx, id string, expectedVersion int64) (Selection, error) {
	canonical, err := uuid.Parse(id)
	if err != nil || expectedVersion < 1 {
		return Selection{}, ErrInvalid
	}
	environment, err := store.New(tx).GetEnvironment(ctx, canonical.String())
	if err != nil {
		return Selection{}, readError(err)
	}
	if environment.Version != expectedVersion {
		return Selection{}, ErrStale
	}
	server, err := store.New(tx).GetServer(ctx, environment.ServerID)
	if err != nil {
		return Selection{}, readError(err)
	}
	if !readiness.Fresh(server, time.Now()) {
		return Selection{}, ErrServer
	}
	return Selection{Environment: environment}, nil
}

// QueueRelease is called only after P05 verifies completed provider authority.
// It does not establish trust merely from supplied Build/artifact fields. Native
// source resolution occurs outside the write transaction; final admission
// rechecks the current binding and commits release+operation+job atomically.
func (s *Service) QueueRelease(ctx context.Context, runtime *operations.Runtime, resolver *compose.Resolver, input ReleaseInput) (store.Release, store.Operation, error) {
	select {
	case s.preparing <- struct{}{}:
		defer func() { <-s.preparing }()
	default:
		return store.Release{}, store.Operation{}, ErrBusy
	}
	canonical, parseErr := uuid.Parse(input.Intent.EnvironmentID)
	if parseErr != nil {
		return store.Release{}, store.Operation{}, ErrInvalid
	}
	input.Intent.EnvironmentID = canonical.String()
	if input.Intent.Kind != operations.Deploy || input.Selection.Environment.ID != input.Intent.EnvironmentID || input.Selection.Environment.Version != input.Intent.ExpectedVersion {
		return store.Release{}, store.Operation{}, ErrInvalid
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	existing, replay, err := runtime.Replay(ctx, tx, input.Intent)
	if err != nil {
		_ = tx.Rollback()
		return store.Release{}, store.Operation{}, err
	}
	if replay {
		release, readErr := store.New(tx).GetRelease(ctx, existing.ReleaseID)
		_ = tx.Rollback()
		return release, existing, readErr
	}
	if err := tx.Rollback(); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	selected := input.Selection.Environment
	if err := s.checkBinding(ctx, store.New(s.database), selected, input.Intent.Automatic); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	project, err := store.New(s.database).GetProject(ctx, selected.ProjectID)
	if err != nil {
		return store.Release{}, store.Operation{}, readError(err)
	}
	build := input.Build
	if build.RepositoryID != project.RepositoryID || build.Branch != selected.Branch || build.SourceSHA != input.Intent.SourceSHA {
		return store.Release{}, store.Operation{}, ErrInvalid
	}
	// Re-admit the resolved set through the same strict parser retained material uses.
	encoded, err := json.Marshal(input.Artifact)
	if err != nil {
		return store.Release{}, store.Operation{}, ErrInvalid
	}
	artifact, err := releases.ParseArtifact(encoded, build.SourceSHA)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	values, err := s.vault.Environment(ctx, securestate.RecordRef{Kind: securestate.EnvironmentKind, OwnerID: selected.ID, Version: selected.SecretVersion})
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	available := make(map[string]bool, len(values))
	for name, value := range values {
		available[name] = value != ""
	}
	network := compose.Network{ProjectName: "vps-" + strings.ReplaceAll(selected.ID, "-", ""), BridgeName: selected.BridgeName}
	prepared, err := resolver.Resolve(ctx, input.Tree, selected.OverridePath, artifact, available, network)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	var domains []*panelv1.Domain
	if json.Unmarshal(selected.DomainsJson, &domains) != nil || len(domains) > 32 {
		return store.Release{}, store.Operation{}, ErrInvalid
	}
	for _, domain := range domains {
		if _, exists := artifact.Services[domain.GetService()]; !exists {
			return store.Release{}, store.Operation{}, ErrInvalid
		}
	}
	if err := boundMaterializedConfig(prepared, values); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	images := make([]*panelv1.ServiceImage, 0, len(prepared.Services))
	for _, name := range prepared.Services {
		service := artifact.Services[name]
		images = append(images, &panelv1.ServiceImage{Service: name, ImageDigest: service.Image})
	}
	imageData, _ := json.Marshal(images)
	// SecretBinding.Secret is a variable name; this metadata never holds plaintext.
	bindings, _ := json.Marshal(prepared.Bindings) // #nosec G117 -- secret references only, never values.
	// Hash only nonsecret config and immutable references, never secret values.
	hash, err := releases.SnapshotSHA256(prepared.Config, selected.DomainsJson, selected.ID, selected.SecretVersion)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	tx, err = s.database.BeginTx(ctx, nil)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, replay, err := runtime.Replay(ctx, tx, input.Intent); err != nil {
		return store.Release{}, store.Operation{}, err
	} else if replay {
		release, readErr := store.New(tx).GetRelease(ctx, existing.ReleaseID)
		return release, existing, readErr
	}
	queries := store.New(tx)
	if err := s.checkBinding(ctx, queries, selected, input.Intent.Automatic); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	id := uuid.NewString()
	if err := queries.CreateRelease(ctx, store.CreateReleaseParams{ID: id, EnvironmentID: selected.ID, ServerID: selected.ServerID, RepositoryID: build.RepositoryID, Branch: build.Branch, SourceSha: build.SourceSHA, EnvironmentVersion: selected.Version, BindingVersion: selected.BindingVersion, ImagesJson: imageData, DomainsJson: selected.DomainsJson, SecretVersion: selected.SecretVersion, ComposeConfig: prepared.Config, SecretBindingsJson: bindings, ConfigSha256: hash, CreatedAt: time.Now().UnixMilli()}); err != nil {
		return store.Release{}, store.Operation{}, fmt.Errorf("store immutable release: %w", err)
	}
	for _, name := range prepared.Services {
		if err := queries.CreateReleaseService(ctx, store.CreateReleaseServiceParams{ReleaseID: id, Name: name, ImageDigest: artifact.Services[name].Image}); err != nil {
			return store.Release{}, store.Operation{}, err
		}
	}
	operation, err := runtime.Enqueue(ctx, tx, input.Intent, operations.Assignment{ServerID: selected.ServerID, EnvironmentID: selected.ID, ReleaseID: id, BindingVersion: selected.BindingVersion})
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	if _, err := routes.ReserveOperationTx(ctx, tx, operation.ID); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	release, err := queries.GetRelease(ctx, id)
	if err != nil {
		return store.Release{}, store.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Release{}, store.Operation{}, err
	}
	return release, operation, nil
}

// Bound the future secret-injected JSON without exposing plaintext to Compose,
// disk or release records. Native config already escapes static dollars; only
// newly inserted values need escaping for the executor's second interpolation.
func boundMaterializedConfig(prepared compose.Prepared, values map[string]string) error {
	if len(prepared.Config) > 2<<20 {
		return ErrCapacity
	}
	var snapshot struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if json.Unmarshal(prepared.Config, &snapshot) != nil {
		return errCorruptMetadata
	}
	size := int64(len(prepared.Config))
	encodedSizes := make(map[string]int64, len(values))
	for _, binding := range prepared.Bindings {
		value, exists := values[binding.Secret]
		if !exists {
			return errCorruptMetadata
		}
		encodedSize, exists := encodedSizes[binding.Secret]
		if !exists {
			encoded, err := json.Marshal(strings.ReplaceAll(value, "$", "$$"))
			if err != nil {
				return errCorruptMetadata
			}
			encodedSize = int64(len(encoded))
			encodedSizes[binding.Secret] = encodedSize
		}
		reference, exists := snapshot.Services[binding.Service].Environment[binding.Variable]
		if !exists {
			return errCorruptMetadata
		}
		encoded, _ := json.Marshal(reference)
		size += encodedSize - int64(len(encoded))
	}
	if size > 2<<20 {
		return ErrCapacity
	}
	return nil
}

func (s *Service) checkBinding(ctx context.Context, queries *store.Queries, selected store.Environment, automatic bool) error {
	current, err := queries.GetEnvironment(ctx, selected.ID)
	if err != nil {
		return readError(err)
	}
	if current.ProjectID != selected.ProjectID || current.BindingVersion != selected.BindingVersion || current.ServerID != selected.ServerID || current.Branch != selected.Branch || current.OverridePath != selected.OverridePath {
		return ErrStale
	}
	if automatic && (!current.AutoDeploy || !selected.AutoDeploy) {
		return ErrStale
	}
	server, err := queries.GetServer(ctx, current.ServerID)
	if err != nil {
		return readError(err)
	}
	if !readiness.Fresh(server, time.Now()) {
		return ErrServer
	}
	return nil
}
func releaseMessage(value store.GetReleaseMetadataRow) (*panelv1.Release, error) {
	var images []*panelv1.ServiceImage
	if json.Unmarshal(value.ImagesJson, &images) != nil {
		return nil, errCorruptMetadata
	}
	var domains []*panelv1.Domain
	if json.Unmarshal(value.DomainsJson, &domains) != nil {
		return nil, errCorruptMetadata
	}
	version, err := apiVersion(value.EnvironmentVersion)
	if err != nil {
		return nil, err
	}
	binding, err := apiVersion(value.BindingVersion)
	if err != nil {
		return nil, err
	}
	return &panelv1.Release{Id: value.ID, EnvironmentId: value.EnvironmentID, Build: &panelv1.BuildIdentity{GithubRepositoryId: value.RepositoryID, Branch: value.Branch, SourceSha: value.SourceSha}, EnvironmentVersion: version, BindingVersion: binding, Images: images, Domains: domains, SecretVersionId: fmt.Sprint(value.SecretVersion), ConfigurationSha256: value.ConfigSha256, CreatedAt: timestamppb.New(time.UnixMilli(value.CreatedAt))}, nil
}
func (s *Service) GetRelease(ctx context.Context, id string) (*panelv1.Release, error) {
	canonical, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrInvalid
	}
	value, err := store.New(s.database).GetReleaseMetadata(ctx, canonical.String())
	if err != nil {
		return nil, readError(err)
	}
	return releaseMessage(value)
}
func (s *Service) ListReleases(ctx context.Context, environmentID string, request *panelv1.PageRequest) (*panelv1.ListReleasesResponse, error) {
	canonical, err := uuid.Parse(environmentID)
	if err != nil {
		return nil, ErrInvalid
	}
	position, size, err := page(request, "releases", canonical.String())
	if err != nil {
		return nil, err
	}
	// A release can contain 64 images and 32 domains; fit the 2 MiB RPC budget.
	if size > 20 {
		size = 20
	}
	values, err := store.New(s.database).ListReleases(ctx, store.ListReleasesParams{EnvironmentID: canonical.String(), CursorTime: position.At, CursorID: position.ID, PageLimit: size + 1})
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListReleasesResponse{Page: &panelv1.PageResponse{}}
	if int64(len(values)) > size {
		last := values[size-1]
		response.Page.NextPageToken = next("releases", canonical.String(), "", last.CreatedAt, last.ID)
		values = values[:size]
	}
	for _, value := range values {
		message, err := releaseMessage(store.GetReleaseMetadataRow(value))
		if err != nil {
			return nil, err
		}
		response.Releases = append(response.Releases, message)
	}
	return response, nil
}
func (s *Service) ListServices(ctx context.Context, environmentID string, request *panelv1.PageRequest) (*panelv1.ListServicesResponse, error) {
	canonical, err := uuid.Parse(environmentID)
	if err != nil {
		return nil, ErrInvalid
	}
	position, size, err := page(request, "services", canonical.String())
	if err != nil {
		return nil, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	environment, err := queries.GetEnvironment(ctx, canonical.String())
	if err != nil {
		return nil, readError(err)
	}
	server, err := queries.GetServer(ctx, environment.ServerID)
	if err != nil {
		return nil, readError(err)
	}
	hostReady := readiness.Fresh(server, time.Now())
	releaseID := environment.CurrentReleaseID.String
	if releaseID == "" {
		latest, err := queries.GetLatestRelease(ctx, canonical.String())
		if errors.Is(err, sql.ErrNoRows) {
			return &panelv1.ListServicesResponse{Page: &panelv1.PageResponse{}}, nil
		}
		if err != nil {
			return nil, err
		}
		releaseID = latest.ID
	}
	if position.Snapshot != "" && position.Snapshot != releaseID {
		return nil, ErrStale
	}
	values, err := queries.ListObservedReleaseServices(ctx, store.ListObservedReleaseServicesParams{EnvironmentID: canonical.String(), ReleaseID: releaseID, AfterName: position.ID, PageLimit: size + 1})
	if err != nil {
		return nil, err
	}
	response := &panelv1.ListServicesResponse{Page: &panelv1.PageResponse{}}
	if int64(len(values)) > size {
		last := values[size-1]
		response.Page.NextPageToken = next("services", canonical.String(), releaseID, 0, last.Name)
		values = values[:size]
	}
	for _, value := range values {
		item := &panelv1.Service{Name: value.Name, EnvironmentId: canonical.String(), State: panelv1.ServiceState_SERVICE_STATE_UNKNOWN, Health: panelv1.ServiceHealth_SERVICE_HEALTH_UNKNOWN}
		if value.ObservedAt.Valid {
			item.ObservedAt = timestamppb.New(time.UnixMilli(value.ObservedAt.Int64))
			item.ObservedImageDigest = value.ObservedImageDigest.String
			item.ObservedReleaseId = value.ObservedReleaseID.String
			age := time.Since(time.UnixMilli(value.ObservedAt.Int64))
			// A deployment receipt is a point-in-time observation, not a monitor.
			if hostReady && value.ExecutionPending == 0 && age >= 0 && age <= 5*time.Minute && value.ObservedImageDigest.String == value.ImageDigest {
				if value.State.String == "running" {
					item.State = panelv1.ServiceState_SERVICE_STATE_RUNNING
				}
				if value.Health.String == "healthy" {
					item.Health = panelv1.ServiceHealth_SERVICE_HEALTH_HEALTHY
				}
			}
		}
		response.Services = append(response.Services, item)
	}
	return response, nil
}
