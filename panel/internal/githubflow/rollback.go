package githubflow

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/google/go-github/v93/github"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// RequestRollback admits an existing immutable release. It neither rebuilds its
// configuration from desired settings nor executes or claims remote recovery.
func (b *Builds) RequestRollback(ctx context.Context, request *panelv1.RollbackEnvironmentRequest) (string, error) {
	version := request.GetExpectedVersion()
	if version < 1 || version > math.MaxInt64 {
		return "", ErrInvalid
	}
	intent, _, err := operations.IntentIdentity(operations.Intent{RequestID: request.GetMutation().GetRequestId(), Kind: operations.Rollback, EnvironmentID: request.GetEnvironmentId(), ExpectedVersion: int64(version), TargetReleaseID: request.GetReleaseId()})
	if err != nil {
		return "", err
	}
	tx, err := b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	_, replay, err := b.runtime.Replay(ctx, tx, intent)
	_ = tx.Rollback()
	if err != nil || replay {
		return intent.RequestID, err
	}
	queries := store.New(b.integration.database)
	environment, err := queries.GetEnvironment(ctx, intent.EnvironmentID)
	if err != nil {
		return "", rollbackReadError(err)
	}
	if environment.Version != intent.ExpectedVersion {
		return "", projects.ErrStale
	}
	if err := rollbackRelease(ctx, queries, intent.TargetReleaseID, environment); err != nil {
		return "", err
	}
	project, err := queries.GetProject(ctx, environment.ProjectID)
	if err != nil {
		return "", err
	}
	client, repository, err := b.integration.selectedClient(ctx, project.RepositoryID, false)
	if err != nil {
		return "", err
	}
	if err := b.integration.verifyWorkflow(ctx, client, repository, project.WorkflowID); err != nil {
		return "", err
	}
	// Include unfinished and other-branch runs: any already existing run must not
	// become an older automatic candidate that immediately undoes this rollback.
	runs, response, err := client.Actions.ListWorkflowRunsByID(ctx, repository.Owner, repository.Name, project.WorkflowID, &github.ListWorkflowRunsOptions{ListOptions: github.ListOptions{PerPage: 1, Page: 1}})
	if err != nil || runs == nil {
		return "", b.integration.failure(ctx, response, err)
	}
	cutoff, attempt := int64(0), int64(0)
	if len(runs.WorkflowRuns) > 0 {
		run := runs.WorkflowRuns[0]
		if run.GetRunNumber() < 1 || run.GetRunAttempt() < 1 || run.GetWorkflowID() != project.WorkflowID || run.GetRepository().GetID() != project.RepositoryID {
			return "", ErrProvider
		}
		cutoff, attempt = int64(run.GetRunNumber()), int64(run.GetRunAttempt())
	}
	tx, err = b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, replay, err := b.runtime.Replay(ctx, tx, intent); err != nil || replay {
		return intent.RequestID, err
	}
	selection, err := b.metadata.CaptureSelection(ctx, tx, intent.EnvironmentID, intent.ExpectedVersion)
	if err != nil {
		return "", err
	}
	current := selection.Environment
	if current.BindingVersion != environment.BindingVersion || current.ServerID != environment.ServerID || current.ProjectID != environment.ProjectID {
		return "", projects.ErrStale
	}
	queries = store.New(tx)
	if err := rollbackRelease(ctx, queries, intent.TargetReleaseID, current); err != nil {
		return "", err
	}
	if err := queries.InitializeBranchProgress(ctx, store.InitializeBranchProgressParams{EnvironmentID: current.ID, BindingVersion: current.BindingVersion}); err != nil {
		return "", err
	}
	progress, err := queries.GetBranchProgress(ctx, current.ID)
	if err != nil {
		return "", err
	}
	if progress.BindingVersion != current.BindingVersion {
		if err := queries.ResetBranchProgress(ctx, store.ResetBranchProgressParams{EnvironmentID: current.ID, BindingVersion: current.BindingVersion}); err != nil {
			return "", err
		}
		progress.ManualCutoff, progress.ManualAttempt = 0, 0
	}
	if cutoff < progress.ManualCutoff || (cutoff == progress.ManualCutoff && attempt < progress.ManualAttempt) {
		cutoff, attempt = progress.ManualCutoff, progress.ManualAttempt
	}
	if err := queries.SetManualBuildCutoff(ctx, store.SetManualBuildCutoffParams{EnvironmentID: current.ID, ManualCutoff: cutoff, ManualAttempt: attempt}); err != nil {
		return "", err
	}
	// Build waiting has no target effects. Like explicit Deploy, a new explicit
	// rollback supersedes these intents; started/unknown target operations still
	// conflict in Enqueue and roll back every write made here.
	if err := queries.SupersedeWaitingForManual(ctx, store.SupersedeWaitingForManualParams{EnvironmentID: current.ID, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		return "", err
	}
	operation, err := b.runtime.Enqueue(ctx, tx, intent, operations.Assignment{ServerID: current.ServerID, EnvironmentID: current.ID, ReleaseID: intent.TargetReleaseID, BindingVersion: current.BindingVersion})
	if err != nil {
		return "", err
	}
	// Require already retained route authority before ReserveOperationTx: rollback
	// must not invent a missing old reservation from today's desired settings.
	if _, err := routes.OperationSnapshot(ctx, queries, operation.ID); err != nil {
		return "", err
	}
	if _, err := routes.ReserveOperationTx(ctx, tx, operation.ID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operation.ID, nil
}

func rollbackRelease(ctx context.Context, queries *store.Queries, id string, environment store.Environment) error {
	release, err := queries.GetReleaseMetadata(ctx, id)
	if err != nil {
		return rollbackReadError(err)
	}
	if release.EnvironmentID != environment.ID || release.ServerID != environment.ServerID {
		return projects.ErrBinding
	}
	return nil
}

func rollbackReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return projects.ErrNotFound
	}
	return err
}
