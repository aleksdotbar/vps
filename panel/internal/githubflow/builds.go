package githubflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const pollInterval = 30 * time.Second

// imageWait bounds how long a deployment waits for its GIT_SHA image tags.
const imageWait = time.Hour

// ImageResolver resolves a public GHCR repository tag to an immutable digest.
type ImageResolver interface {
	Resolve(ctx context.Context, repository, tag string) (string, error)
}

type Builds struct {
	integration *Service
	metadata    *projects.Service
	resolver    *compose.Resolver
	runtime     *operations.Runtime
	images      ImageResolver
}

func NewBuilds(integration *Service, metadata *projects.Service, resolver *compose.Resolver, runtime *operations.Runtime, images ImageResolver) *Builds {
	return &Builds{integration, metadata, resolver, runtime, images}
}
func (b *Builds) Register(workers *river.Workers) ([]*river.PeriodicJob, error) {
	if err := river.AddWorkerSafely(workers, &buildWorker{builds: b}); err != nil {
		return nil, err
	}
	if err := river.AddWorkerSafely(workers, &discoveryWorker{builds: b}); err != nil {
		return nil, err
	}
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(pollInterval), func() (river.JobArgs, *river.InsertOpts) {
		return operations.DiscoveryArgs{}, &river.InsertOpts{Queue: "github", MaxAttempts: 5, UniqueOpts: operations.DiscoveryUnique()}
	}, &river.PeriodicJobOpts{ID: "github-discovery", RunOnStart: true})}, nil
}
func (b *Builds) RequestDeploy(ctx context.Context, request *panelv1.DeployEnvironmentRequest) (string, error) {
	version := request.GetExpectedVersion()
	if version < 1 || version > math.MaxInt64 {
		return "", ErrInvalid
	}
	intent, identity, err := operations.IntentIdentity(operations.Intent{RequestID: request.GetMutation().GetRequestId(), Kind: operations.Deploy, EnvironmentID: request.GetEnvironmentId(), ExpectedVersion: int64(version), SourceSHA: request.GetSourceSha()})
	if err != nil {
		return "", err
	}
	tx, err := b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	replay, err := b.replay(ctx, tx, intent, identity)
	_ = tx.Rollback()
	if err != nil {
		return "", err
	}
	if replay {
		return intent.RequestID, nil
	}
	environment, err := store.New(b.integration.database).GetEnvironment(ctx, intent.EnvironmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", projects.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	project, err := store.New(b.integration.database).GetProject(ctx, environment.ProjectID)
	if err != nil {
		return "", err
	}
	// The current branch head becomes handled: automatic discovery must not
	// replace this explicit intent with the same head, only with a newer push.
	head, err := b.branchHead(ctx, project.RepositoryID, environment.Branch)
	if err != nil {
		return "", err
	}
	tx, err = b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	replay, err = b.replay(ctx, tx, intent, identity)
	if err != nil {
		return "", err
	}
	if replay {
		return intent.RequestID, nil
	}
	selection, err := b.metadata.CaptureSelection(ctx, tx, intent.EnvironmentID, intent.ExpectedVersion)
	if err != nil {
		return "", err
	}
	queries := store.New(tx)
	if err := queries.SupersedeWaitingForManual(ctx, store.SupersedeWaitingForManualParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: intent.EnvironmentID}); err != nil {
		return "", err
	}
	if err := queries.SupersedeQueuedAutomaticForManual(ctx, store.SupersedeQueuedAutomaticForManualParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: intent.EnvironmentID}); err != nil {
		return "", err
	}
	if err := queries.InitializeBranchProgress(ctx, store.InitializeBranchProgressParams{EnvironmentID: intent.EnvironmentID, BindingVersion: selection.Environment.BindingVersion}); err != nil {
		return "", err
	}
	progress, err := queries.GetBranchProgress(ctx, intent.EnvironmentID)
	if err != nil {
		return "", err
	}
	if progress.BindingVersion != selection.Environment.BindingVersion {
		if err := queries.ResetBranchProgress(ctx, store.ResetBranchProgressParams{EnvironmentID: intent.EnvironmentID, BindingVersion: selection.Environment.BindingVersion}); err != nil {
			return "", err
		}
	}
	if err := queries.SetHandledBranchHead(ctx, store.SetHandledBranchHeadParams{HandledSha: head, EnvironmentID: intent.EnvironmentID}); err != nil {
		return "", err
	}
	if err := b.insert(ctx, tx, intent, identity, selection); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return intent.RequestID, nil
}
func (b *Builds) branchHead(ctx context.Context, repositoryID int64, name string) (string, error) {
	client, repository, err := b.integration.selectedClient(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	branch, response, err := client.Repositories.GetBranch(ctx, repository.Owner, repository.Name, name, 0)
	if err != nil || branch == nil {
		return "", b.integration.failure(ctx, response, err)
	}
	sha := branch.Commit.GetSHA()
	if !sourceSHA(sha) {
		return "", ErrProvider
	}
	return sha, nil
}
func (b *Builds) replay(ctx context.Context, tx *sql.Tx, intent operations.Intent, identity []byte) (bool, error) {
	waiting, err := store.New(tx).GetWaitingBuild(ctx, intent.RequestID)
	if err == nil {
		if !bytes.Equal(waiting.Fingerprint, identity) {
			return false, operations.ErrRequestReused
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, found, err := b.runtime.Replay(ctx, tx, intent)
	return found, err
}
func (b *Builds) insert(ctx context.Context, tx *sql.Tx, intent operations.Intent, identity []byte, selection projects.Selection) error {
	queries := store.New(tx)
	capacity, err := queries.WaitingBuildCapacity(ctx)
	if err != nil {
		return err
	}
	if capacity.Retained >= 100000 || capacity.Unresolved >= 128 {
		return operations.ErrCapacity
	}
	if _, err := queries.ActiveWaitingBuildForEnvironment(ctx, intent.EnvironmentID); err == nil {
		return operations.ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	pending, err := queries.HostSetupPending(ctx, selection.Environment.ServerID)
	if err != nil {
		return err
	}
	if pending {
		return operations.ErrConflict
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return ErrInvalid
	}
	selected, err := json.Marshal(selection)
	if err != nil {
		return ErrInvalid
	}
	now := time.Now()
	if err := queries.CreateWaitingBuild(ctx, store.CreateWaitingBuildParams{ID: intent.RequestID, EnvironmentID: intent.EnvironmentID, ServerID: selection.Environment.ServerID, IntentJson: encoded, Fingerprint: identity, SelectionJson: selected, BindingVersion: selection.Environment.BindingVersion, Automatic: intent.Automatic, NextPollAt: now.UnixMilli(), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), ExpiresAt: now.Add(imageWait).UnixMilli()}); err != nil {
		return err
	}
	jobID, err := b.runtime.EnqueueBuild(ctx, tx, intent.RequestID)
	if err != nil {
		return err
	}
	return queries.SetWaitingBuildJob(ctx, store.SetWaitingBuildJobParams{RiverJobID: jobID, ID: intent.RequestID})
}
func automaticID(environmentID string, bindingVersion int64, sha string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(environmentID+":"+strconv.FormatInt(bindingVersion, 10)+":"+sha)).String()
}

func (b *Builds) GetDeploymentSource(ctx context.Context, id string, expected uint64) (*panelv1.GetDeploymentSourceResponse, error) {
	if expected < 1 || expected > math.MaxInt64 {
		return nil, ErrInvalid
	}
	canonical, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrInvalid
	}
	environment, err := store.New(b.integration.database).GetEnvironment(ctx, canonical.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, projects.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if environment.Version != int64(expected) {
		return nil, projects.ErrStale
	}
	project, err := store.New(b.integration.database).GetProject(ctx, environment.ProjectID)
	if err != nil {
		return nil, err
	}
	sha, err := b.branchHead(ctx, project.RepositoryID, environment.Branch)
	if err != nil {
		return nil, err
	}
	current, err := store.New(b.integration.database).GetEnvironment(ctx, environment.ID)
	if err != nil {
		return nil, err
	}
	if current.Version != environment.Version || current.BindingVersion != environment.BindingVersion {
		return nil, projects.ErrStale
	}
	return &panelv1.GetDeploymentSourceResponse{SourceSha: sha, Branch: environment.Branch, EnvironmentVersion: expected}, nil
}
