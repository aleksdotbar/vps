package githubflow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type buildWorker struct {
	river.WorkerDefaults[operations.BuildArgs]
	builds *Builds
}

func (*buildWorker) Timeout(*river.Job[operations.BuildArgs]) time.Duration { return time.Minute }
func (w *buildWorker) Work(ctx context.Context, job *river.Job[operations.BuildArgs]) error {
	delay, err := w.builds.PollBuild(ctx, job.Args.RequestID)
	if err != nil {
		return err
	}
	if delay > 0 {
		return river.JobSnooze(delay)
	}
	return nil
}
func (b *Builds) finish(ctx context.Context, id, state, reason string) error {
	return store.New(b.integration.database).FinishWaitingBuild(ctx, store.FinishWaitingBuildParams{ID: id, State: state, Reason: reason, UpdatedAt: time.Now().UnixMilli()})
}
func (b *Builds) schedule(ctx context.Context, waiting store.WaitingBuild, delay time.Duration, page, failures int64) (time.Duration, error) {
	if delay < pollInterval {
		delay = pollInterval
	}
	wake := min(delay, time.Hour)
	if page < 1 || page > 10 {
		return 0, b.finish(ctx, waiting.ID, "failed", "discovery_limit")
	}
	if failures >= 10 || waiting.PollCount >= 2879 {
		state := "failed"
		if waiting.State == "dispatch_unknown" {
			state = "needs_attention"
		}
		return 0, b.finish(ctx, waiting.ID, state, "polling_exhausted")
	}
	now := time.Now()
	err := store.New(b.integration.database).ScheduleWaitingBuild(ctx, store.ScheduleWaitingBuildParams{ID: waiting.ID, NextPollAt: now.Add(delay).UnixMilli(), DiscoveryPage: page, Failures: failures, UpdatedAt: now.UnixMilli()})
	return wake, err
}
func (b *Builds) retry(ctx context.Context, waiting store.WaitingBuild, cause error) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	b.integration.clearTokensOnFailure(cause)
	if errors.Is(cause, routes.ErrOwnership) {
		return 0, b.finish(ctx, waiting.ID, "failed", "route_ownership_conflict")
	}
	if errors.Is(cause, routes.ErrSetup) {
		return 0, b.finish(ctx, waiting.ID, "failed", "route_setup_required")
	}
	if errors.Is(cause, routes.ErrCapacity) {
		return 0, b.finish(ctx, waiting.ID, "failed", "route_capacity")
	}
	if errors.Is(cause, operations.ErrConflict) || errors.Is(cause, projects.ErrBusy) || errors.Is(cause, ErrBusy) {
		return b.schedule(ctx, waiting, pollInterval, waiting.DiscoveryPage, 0)
	}
	if errors.Is(cause, projects.ErrStale) {
		return 0, b.finish(ctx, waiting.ID, "superseded", "binding_changed")
	}
	if errors.Is(cause, ErrOwner) || errors.Is(cause, ErrSetup) || errors.Is(cause, ErrArtifact) || errors.Is(cause, compose.ErrUnsafe) || errors.Is(cause, compose.ErrMissingSecret) || errors.Is(cause, releases.ErrInvalidArtifact) {
		return 0, b.finish(ctx, waiting.ID, "failed", "incompatible_build")
	}
	var failure *Failure
	if errors.As(cause, &failure) && failure.Permanent && waiting.State != "dispatch_unknown" {
		return 0, b.finish(ctx, waiting.ID, "failed", "provider_rejected")
	}
	var limited *Failure
	if errors.As(cause, &limited) && limited.RateLimited {
		return b.schedule(ctx, waiting, limited.After, waiting.DiscoveryPage, waiting.Failures)
	}
	count := waiting.Failures + 1
	delay := pollInterval * time.Duration(1<<min(count, 5))
	if errors.As(cause, &failure) && failure.After > delay {
		delay = failure.After
	}
	return b.schedule(ctx, waiting, delay, waiting.DiscoveryPage, count)
}
func (b *Builds) PollBuild(ctx context.Context, id string) (time.Duration, error) {
	waiting, err := store.New(b.integration.database).GetWaitingBuild(ctx, id)
	if err != nil {
		return 0, err
	}
	if waiting.State != "waiting" && waiting.State != "dispatch_unknown" {
		return 0, nil
	}
	now := time.Now()
	if waiting.ExpiresAt <= now.UnixMilli() || waiting.PollCount >= 2880 {
		state := "failed"
		if waiting.State == "dispatch_unknown" {
			state = "needs_attention"
		}
		return 0, b.finish(ctx, id, state, "build_timeout")
	}
	if waiting.NextPollAt > now.UnixMilli() {
		return min(time.Until(time.UnixMilli(waiting.NextPollAt)), time.Hour), nil
	}

	var intent operations.Intent
	var selection projects.Selection
	if json.Unmarshal(waiting.IntentJson, &intent) != nil || json.Unmarshal(waiting.SelectionJson, &selection) != nil || selection.Environment.ID != waiting.EnvironmentID {
		return 0, b.finish(ctx, id, "needs_attention", "invalid_persisted_selection")
	}
	current, err := store.New(b.integration.database).GetEnvironment(ctx, waiting.EnvironmentID)
	if err != nil {
		return 0, err
	}
	if current.BindingVersion != waiting.BindingVersion || (waiting.Automatic && !current.AutoDeploy) {
		return 0, b.finish(ctx, id, "superseded", "binding_changed")
	}
	project, err := store.New(b.integration.database).GetProject(ctx, selection.Environment.ProjectID)
	if err != nil {
		return 0, err
	}
	client, repository, err := b.integration.selectedClient(ctx, project.RepositoryID, false)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}

	if waiting.RunID == 0 {
		return b.discoverBuild(ctx, client, repository, project, selection, intent, waiting)
	}
	if waiting.RunAttempt < 1 || waiting.RunAttempt > math.MaxInt32 {
		return 0, b.finish(ctx, id, "failed", "invalid_build_identity")
	}
	run, response, err := client.Actions.GetWorkflowRunAttempt(ctx, repository.Owner, repository.Name, waiting.RunID, int(waiting.RunAttempt), nil)
	if err != nil || run == nil {
		return b.retry(ctx, waiting, b.integration.failure(ctx, response, err))
	}
	if !eligibleRun(run, project, selection.Environment.Branch, intent.SourceSHA, !waiting.Automatic) || run.GetID() != waiting.RunID || int64(run.GetRunAttempt()) != waiting.RunAttempt {
		return 0, b.finish(ctx, id, "failed", "untrusted_build")
	}
	if run.GetStatus() != "completed" {
		return b.schedule(ctx, waiting, pollInterval, 1, 0)
	}
	if run.GetConclusion() != "success" {
		return 0, b.finish(ctx, id, "failed", "build_failed")
	}
	// Avoid repeated downloads/native config while an earlier effect is active.
	conflicts, err := store.New(b.integration.database).UnresolvedServerOperations(ctx, waiting.ServerID)
	if err != nil {
		return 0, err
	}
	for _, operation := range conflicts {
		if operation.ID != id && (operation.Kind == string(operations.Enroll) || operation.EnvironmentID == waiting.EnvironmentID) {
			return b.schedule(ctx, waiting, pollInterval, 1, 0)
		}
	}
	artifact, err := b.artifact(ctx, client, repository, run, waiting.ID)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	tree, cleanup, err := b.source(ctx, client, repository, selection, intent.SourceSHA)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	defer cleanup()
	_, _, err = b.metadata.QueueRelease(ctx, b.runtime, b.resolver, projects.ReleaseInput{Intent: intent, Selection: selection, Build: projects.Build{RepositoryID: project.RepositoryID, WorkflowID: project.WorkflowID, RunID: run.GetID(), RunAttempt: int64(run.GetRunAttempt()), Branch: run.GetHeadBranch(), SourceSHA: run.GetHeadSHA()}, Tree: tree, Artifact: artifact})
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	return 0, nil
}
func eligibleRun(run *github.WorkflowRun, project store.Project, branch, sha string, manual bool) bool {
	if !sourceSHA(sha) || run == nil || run.GetID() < 1 || run.GetRunAttempt() < 1 || run.GetRunNumber() < 1 || run.GetWorkflowID() != project.WorkflowID || run.Repository.GetID() != project.RepositoryID || run.HeadRepository.GetID() != project.RepositoryID || run.GetHeadBranch() != branch || run.GetHeadSHA() != sha {
		return false
	}
	if run.GetEvent() != "push" && (!manual || run.GetEvent() != "workflow_dispatch") {
		return false
	}
	path := run.GetPath()
	return path == ".github/workflows/panel-build.yaml" || path == ".github/workflows/panel-build.yaml@refs/heads/"+branch || path == ".github/workflows/panel-build.yaml@"+branch
}
func (b *Builds) discoverBuild(ctx context.Context, client *github.Client, repository store.GithubRepository, project store.Project, selection projects.Selection, intent operations.Intent, waiting store.WaitingBuild) (time.Duration, error) {
	runs, response, err := client.Actions.ListWorkflowRunsByID(ctx, repository.Owner, repository.Name, project.WorkflowID, &github.ListWorkflowRunsOptions{Branch: selection.Environment.Branch, HeadSHA: intent.SourceSHA, ListOptions: github.ListOptions{Page: int(waiting.DiscoveryPage), PerPage: 100}})
	if err != nil || runs == nil {
		return b.retry(ctx, waiting, b.integration.failure(ctx, response, err))
	}
	var selected *github.WorkflowRun
	for _, run := range runs.WorkflowRuns {
		if !eligibleRun(run, project, selection.Environment.Branch, intent.SourceSHA, true) {
			continue
		}
		if waiting.DispatchStartedAt > 0 && (run.GetEvent() != "workflow_dispatch" || run.GetDisplayTitle() != "panel-build "+waiting.ID) {
			continue
		}
		if waiting.DispatchStartedAt == 0 && run.GetStatus() == "completed" && run.GetConclusion() != "success" {
			continue
		}
		if selected == nil || (run.GetConclusion() == "success" && selected.GetConclusion() != "success") || (run.GetConclusion() == selected.GetConclusion() && run.GetRunNumber() > selected.GetRunNumber()) {
			selected = run
		}
	}
	if selected != nil {
		if err := store.New(b.integration.database).SetWaitingBuildRun(ctx, store.SetWaitingBuildRunParams{ID: waiting.ID, RunID: selected.GetID(), RunAttempt: int64(selected.GetRunAttempt()), RunNumber: int64(selected.GetRunNumber()), UpdatedAt: time.Now().UnixMilli()}); err != nil {
			return 0, err
		}
		return b.schedule(ctx, waiting, pollInterval, 1, 0)
	}
	if response.NextPage != 0 {
		return b.schedule(ctx, waiting, pollInterval, int64(response.NextPage), 0)
	}
	if waiting.DispatchStartedAt > 0 {
		if time.Since(time.UnixMilli(waiting.DispatchStartedAt)) > 10*time.Minute {
			return 0, b.finish(ctx, waiting.ID, "needs_attention", "dispatch_outcome_unknown")
		}
		return b.schedule(ctx, waiting, pollInterval, 1, 0)
	}
	if waiting.Automatic {
		return 0, b.finish(ctx, waiting.ID, "failed", "build_unavailable")
	}
	return b.dispatch(ctx, repository, project, selection, intent, waiting)
}
func (b *Builds) dispatch(ctx context.Context, repository store.GithubRepository, project store.Project, selection projects.Selection, intent operations.Intent, waiting store.WaitingBuild) (time.Duration, error) {
	client, _, err := b.integration.selectedClient(ctx, project.RepositoryID, true)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	if err := b.integration.verifyWorkflow(ctx, client, repository, project.WorkflowID); err != nil {
		return b.retry(ctx, waiting, err)
	}
	branch, response, err := client.Repositories.GetBranch(ctx, repository.Owner, repository.Name, selection.Environment.Branch, 0)
	if err != nil || branch == nil {
		return b.retry(ctx, waiting, b.integration.failure(ctx, response, err))
	}
	if branch.Commit.GetSHA() != intent.SourceSHA {
		return 0, b.finish(ctx, waiting.ID, "failed", "branch_head_changed")
	}
	tx, err := b.integration.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.New(tx).GetEnvironment(ctx, waiting.EnvironmentID)
	if err != nil {
		return 0, err
	}
	if current.BindingVersion != waiting.BindingVersion {
		return 0, projects.ErrStale
	}
	changed, err := store.New(tx).StartWaitingBuildDispatch(ctx, store.StartWaitingBuildDispatchParams{ID: waiting.ID, DispatchStartedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if changed != 1 {
		return pollInterval, nil
	}
	details, response, err := client.Actions.CreateWorkflowDispatchEventByID(ctx, repository.Owner, repository.Name, project.WorkflowID, github.CreateWorkflowDispatchEventRequest{Ref: selection.Environment.Branch, Inputs: map[string]any{"request_id": waiting.ID}, ReturnRunDetails: new(true)})
	// The persisted dispatch-start guard survives lost responses and restarts.
	// Reconcile this request marker before any further dispatch; never blind retry.
	waiting.State = "dispatch_unknown"
	if err != nil {
		return b.retry(ctx, waiting, b.integration.failure(ctx, response, err))
	}
	if details == nil || details.GetWorkflowRunID() < 1 {
		return b.schedule(ctx, waiting, pollInterval, 1, 0)
	}
	// Attempt identity comes from GetWorkflowRun on the next poll, not dispatch.
	run, response, err := client.Actions.GetWorkflowRunByID(ctx, repository.Owner, repository.Name, details.GetWorkflowRunID())
	if err != nil || run == nil {
		return b.retry(ctx, waiting, b.integration.failure(ctx, response, err))
	}
	if !eligibleRun(run, project, selection.Environment.Branch, intent.SourceSHA, true) || run.GetEvent() != "workflow_dispatch" || run.GetDisplayTitle() != "panel-build "+waiting.ID {
		return 0, b.finish(ctx, waiting.ID, "failed", "untrusted_build")
	}
	if err := store.New(b.integration.database).SetWaitingBuildRun(ctx, store.SetWaitingBuildRunParams{ID: waiting.ID, RunID: run.GetID(), RunAttempt: int64(run.GetRunAttempt()), RunNumber: int64(run.GetRunNumber()), UpdatedAt: time.Now().UnixMilli()}); err != nil {
		return 0, err
	}
	return b.schedule(ctx, waiting, pollInterval, 1, 0)
}

func sourceSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20 && strings.ToLower(value) == value
}
