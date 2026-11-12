package githubflow

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

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
	// The current branch head must not immediately redeploy over this rollback;
	// only a newer push is eligible for automatic deployment again.
	head, err := b.branchHead(ctx, project.RepositoryID, environment.Branch)
	if err != nil {
		return "", err
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
	}
	if err := queries.SetHandledBranchHead(ctx, store.SetHandledBranchHeadParams{EnvironmentID: current.ID, HandledSha: head}); err != nil {
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
