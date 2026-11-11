package githubflow

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/google/go-github/v93/github"
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
func newer(number, attempt, previousNumber, previousAttempt int64) bool {
	return number > previousNumber || (number == previousNumber && attempt > previousAttempt)
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
	client, repository, err := b.integration.selectedClient(ctx, project.RepositoryID, false)
	if err != nil {
		return b.deferDiscovery(ctx, progress, err)
	}

	runs, response, err := client.Actions.ListWorkflowRunsByID(ctx, repository.Owner, repository.Name, project.WorkflowID, &github.ListWorkflowRunsOptions{Branch: environment.Branch, Event: "push", Status: "success", ListOptions: github.ListOptions{Page: 1, PerPage: 100}})
	if err != nil || runs == nil {
		return b.deferDiscovery(ctx, progress, b.integration.failure(ctx, response, err))
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
	if !current.AutoDeploy || current.BindingVersion != environment.BindingVersion {
		return nil
	}
	// Read fresh progress after provider IO: an explicit manual request may have
	// advanced its source/run cutoff and cleared candidates during this call.
	progress, err = queries.GetBranchProgress(ctx, environmentID)
	if err != nil {
		return err
	}
	for _, run := range runs.WorkflowRuns {
		number, attempt := int64(run.GetRunNumber()), int64(run.GetRunAttempt())
		if run.GetStatus() != "completed" || run.GetConclusion() != "success" || !eligibleRun(run, project, current.Branch, run.GetHeadSHA(), false) || !newer(number, attempt, progress.HighWaterRun, progress.HighWaterAttempt) || !newer(number, attempt, progress.ManualCutoff, progress.ManualAttempt) {
			continue
		}
		if progress.CandidateRunID == 0 || newer(number, attempt, progress.CandidateRunNumber, progress.CandidateAttempt) {
			progress.CandidateRunID = run.GetID()
			progress.CandidateAttempt = attempt
			progress.CandidateRunNumber = number
			progress.CandidateSha = run.GetHeadSHA()
		}
	}
	// Automatic discovery samples the latest100 successful branch pushes every
	// cycle. Manual exact-SHA search owns bounded historical pagination.
	progress.DiscoveryPage = 1
	progress.NextPollAt = time.Now().Add(pollInterval).UnixMilli()
	if err := b.admitCandidate(ctx, tx, current, &progress); err != nil {
		return err
	}
	if err := saveProgress(ctx, queries, progress); err != nil {
		return err
	}
	return tx.Commit()
}
func saveProgress(ctx context.Context, queries *store.Queries, progress store.GithubBranchProgress) error {
	return queries.SaveBranchProgress(ctx, store.SaveBranchProgressParams{EnvironmentID: progress.EnvironmentID, DiscoveryPage: progress.DiscoveryPage, HighWaterRun: progress.HighWaterRun, HighWaterAttempt: progress.HighWaterAttempt, CandidateRunID: progress.CandidateRunID, CandidateAttempt: progress.CandidateAttempt, CandidateRunNumber: progress.CandidateRunNumber, CandidateSha: progress.CandidateSha, NextPollAt: progress.NextPollAt})
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
func (b *Builds) admitCandidate(ctx context.Context, tx *sql.Tx, environment store.Environment, progress *store.GithubBranchProgress) error {
	if progress.CandidateRunID > 0 && (!newer(progress.CandidateRunNumber, progress.CandidateAttempt, progress.HighWaterRun, progress.HighWaterAttempt) || !newer(progress.CandidateRunNumber, progress.CandidateAttempt, progress.ManualCutoff, progress.ManualAttempt)) {
		progress.CandidateRunID = 0
		return nil
	}
	if progress.CandidateRunID < 1 {
		return nil
	}
	queries := store.New(tx)
	if existing, err := queries.ActiveWaitingBuildForEnvironment(ctx, environment.ID); err == nil {
		if !existing.Automatic || !newer(progress.CandidateRunNumber, progress.CandidateAttempt, existing.RunNumber, existing.RunAttempt) {
			return nil
		}
		if err := queries.FinishWaitingBuild(ctx, store.FinishWaitingBuildParams{ID: existing.ID, State: "superseded", Reason: "newer_build", UpdatedAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	conflicts, err := queries.UnresolvedServerOperations(ctx, environment.ServerID)
	if err != nil {
		return err
	}
	for _, operation := range conflicts {
		if operation.Kind == string(operations.Enroll) {
			return nil
		}
		if operation.EnvironmentID != environment.ID {
			continue
		}
		if !operation.Automatic || operation.Started || operation.State != "queued" {
			return nil
		}
		previous, err := queries.GetWaitingBuild(ctx, operation.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !newer(progress.CandidateRunNumber, progress.CandidateAttempt, previous.RunNumber, previous.RunAttempt) {
			return nil
		}
		if err := queries.SupersedeQueuedAutomaticCandidate(ctx, store.SupersedeQueuedAutomaticCandidateParams{ID: operation.ID, UpdatedAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
	}
	id := automaticID(environment.ID, environment.BindingVersion, progress.CandidateRunID, progress.CandidateAttempt)
	if _, err := queries.GetWaitingBuild(ctx, id); err == nil {
		progress.HighWaterRun = progress.CandidateRunNumber
		progress.HighWaterAttempt = progress.CandidateAttempt
		progress.CandidateRunID = 0
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	selection, err := b.metadata.CaptureSelection(ctx, tx, environment.ID, environment.Version)
	if errors.Is(err, projects.ErrServer) {
		return nil
	}
	if err != nil {
		return err
	}
	intent, identity, err := operations.IntentIdentity(operations.Intent{RequestID: id, Kind: operations.Deploy, EnvironmentID: environment.ID, ExpectedVersion: environment.Version, SourceSHA: progress.CandidateSha, Automatic: true})
	if err != nil {
		return err
	}
	if err := b.insert(ctx, tx, intent, identity, selection, progress.CandidateRunID, progress.CandidateAttempt, progress.CandidateRunNumber); err != nil {
		// Capacity/conflicts leave the persisted newest candidate for a later poll.
		if errors.Is(err, operations.ErrCapacity) || errors.Is(err, operations.ErrConflict) {
			return nil
		}
		return err
	}
	progress.HighWaterRun = progress.CandidateRunNumber
	progress.HighWaterAttempt = progress.CandidateAttempt
	progress.CandidateRunID = 0
	return nil
}
