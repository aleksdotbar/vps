package githubflow

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type discoveryWorker struct {
	river.WorkerDefaults[operations.DiscoveryArgs]
	builds *Builds
}

func (*discoveryWorker) Timeout(*river.Job[operations.DiscoveryArgs]) time.Duration {
	return time.Minute
}
func (w *discoveryWorker) Work(ctx context.Context, job *river.Job[operations.DiscoveryArgs]) error {
	if job.Args.EnvironmentID == "" {
		return w.builds.sweep(ctx)
	}
	err := w.builds.Discover(ctx, job.Args.EnvironmentID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("GitHub discovery could not be persisted")
	}
	return nil
}
func (b *Builds) sweep(ctx context.Context) error {
	queries := store.New(b.integration.database)
	waiting, err := queries.PollableWaitingBuilds(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(waiting))
	for _, request := range waiting {
		ids = append(ids, request.RiverJobID)
	}
	terminal, err := b.runtime.BuildJobsTerminal(ctx, ids)
	if err != nil {
		return err
	}
	for _, request := range waiting {
		if terminal[request.RiverJobID] {
			if err := b.finish(ctx, request.ID, "needs_attention", "polling_exhausted"); err != nil {
				return err
			}
		}
	}
	environments, err := queries.AutoDeployEnvironments(ctx)
	if err != nil {
		return err
	}
	tx, err := b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, environment := range environments {
		if err := b.runtime.EnqueueDiscovery(ctx, tx, environment.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (b *Builds) Discover(ctx context.Context, environmentID string) error {
	queries := store.New(b.integration.database)
	environment, err := queries.GetEnvironment(ctx, environmentID)
	if err != nil {
		return err
	}
	if !environment.AutoDeploy {
		return nil
	}
	tx, err := b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := store.New(tx).InitializeBranchProgress(ctx, store.InitializeBranchProgressParams{EnvironmentID: environmentID, BindingVersion: environment.BindingVersion}); err != nil {
		_ = tx.Rollback()
		return err
	}
	progress, err := store.New(tx).GetBranchProgress(ctx, environmentID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if progress.BindingVersion != environment.BindingVersion {
		err = store.New(tx).ResetBranchProgress(ctx, store.ResetBranchProgressParams{EnvironmentID: environmentID, BindingVersion: environment.BindingVersion})
		if err == nil {
			progress, err = store.New(tx).GetBranchProgress(ctx, environmentID)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if progress.NextPollAt > time.Now().UnixMilli() {
		return nil
	}
	project, err := queries.GetProject(ctx, environment.ProjectID)
	if err != nil {
		return err
	}
	head, err := b.branchHead(ctx, project.RepositoryID, environment.Branch)
	if err != nil {
		return b.deferDiscovery(ctx, progress, err)
	}
	tx, err = b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries = store.New(tx)
	current, err := queries.GetEnvironment(ctx, environmentID)
	if err != nil {
		return err
	}
	if !current.AutoDeploy || current.BindingVersion != environment.BindingVersion || current.Branch != environment.Branch {
		return nil
	}
	// Read fresh progress after provider IO: an explicit deploy or rollback may
	// have marked this head handled during the call.
	progress, err = queries.GetBranchProgress(ctx, environmentID)
	if err != nil {
		return err
	}
	if head != progress.HandledSha {
		admitted, err := b.admitHead(ctx, tx, current, head)
		if err != nil {
			return err
		}
		if admitted {
			progress.HandledSha = head
		}
	}
	progress.NextPollAt = time.Now().Add(pollInterval).UnixMilli()
	if err := saveProgress(ctx, queries, progress); err != nil {
		return err
	}
	return tx.Commit()
}
func saveProgress(ctx context.Context, queries *store.Queries, progress store.GithubBranchProgress) error {
	return queries.SaveBranchProgress(ctx, store.SaveBranchProgressParams{EnvironmentID: progress.EnvironmentID, HandledSha: progress.HandledSha, NextPollAt: progress.NextPollAt})
}
func (b *Builds) deferDiscovery(ctx context.Context, progress store.GithubBranchProgress, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	delay := pollInterval
	reason := "temporary_provider_failure"
	var permanent *Failure
	if errors.Is(cause, ErrOwner) || errors.Is(cause, ErrSetup) || (errors.As(cause, &permanent) && permanent.Permanent) {
		delay = 5 * time.Minute
		reason = "integration_access_requires_attention"
	}
	b.integration.clearTokensOnFailure(cause)
	var failure *Failure
	if errors.As(cause, &failure) && failure.After > delay {
		delay = failure.After
	}

	progress.NextPollAt = time.Now().Add(delay).UnixMilli()
	slog.WarnContext(ctx, "GitHub automatic build discovery deferred", "environment_id", progress.EnvironmentID, "reason", reason, "retry_after_seconds", int64(delay.Seconds()))
	return store.New(b.integration.database).DeferBranchPoll(ctx, store.DeferBranchPollParams{NextPollAt: progress.NextPollAt, EnvironmentID: progress.EnvironmentID, BindingVersion: progress.BindingVersion})
}

// admitHead queues an automatic deployment for a new branch head, superseding
// older automatic work that has not started. It reports false when the head
// must be retried later (capacity, conflicts, server not ready).
func (b *Builds) admitHead(ctx context.Context, tx *sql.Tx, environment store.Environment, head string) (bool, error) {
	queries := store.New(tx)
	id := automaticID(environment.ID, environment.BindingVersion, head)
	if existing, err := queries.ActiveWaitingBuildForEnvironment(ctx, environment.ID); err == nil {
		if existing.ID == id {
			return true, nil
		}
		if !existing.Automatic {
			return false, nil
		}
		if err := queries.FinishWaitingBuild(ctx, store.FinishWaitingBuildParams{ID: existing.ID, State: "superseded", Reason: "newer_build", UpdatedAt: time.Now().UnixMilli()}); err != nil {
			return false, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	conflicts, err := queries.UnresolvedServerOperations(ctx, environment.ServerID)
	if err != nil {
		return false, err
	}
	for _, operation := range conflicts {
		if operation.Kind == string(operations.Enroll) {
			return false, nil
		}
		if operation.EnvironmentID != environment.ID {
			continue
		}
		if !operation.Automatic || operation.Started || operation.State != "queued" {
			return false, nil
		}
		if err := queries.SupersedeQueuedAutomaticCandidate(ctx, store.SupersedeQueuedAutomaticCandidateParams{ID: operation.ID, UpdatedAt: time.Now().UnixMilli()}); err != nil {
			return false, err
		}
	}
	if _, err := queries.GetWaitingBuild(ctx, id); err == nil {
		return true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	selection, err := b.metadata.CaptureSelection(ctx, tx, environment.ID, environment.Version)
	if errors.Is(err, projects.ErrServer) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	intent, identity, err := operations.IntentIdentity(operations.Intent{RequestID: id, Kind: operations.Deploy, EnvironmentID: environment.ID, ExpectedVersion: environment.Version, SourceSHA: head, Automatic: true})
	if err != nil {
		return false, err
	}
	if err := b.insert(ctx, tx, intent, identity, selection); err != nil {
		// Capacity/conflicts leave the head unhandled for a later poll.
		if errors.Is(err, operations.ErrCapacity) || errors.Is(err, operations.ErrConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
