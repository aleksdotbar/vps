package githubflow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/registry"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// Waiting reasons carrying the image reference the deployment is blocked on.
const (
	waitingForImage = "waiting_for_image:"
	imageNotFound   = "image_not_found:"
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
func (b *Builds) schedule(ctx context.Context, waiting store.WaitingBuild, delay time.Duration, reason string, failures int64) (time.Duration, error) {
	if delay < pollInterval {
		delay = pollInterval
	}
	if failures >= 10 || waiting.PollCount >= 2879 {
		return 0, b.finish(ctx, waiting.ID, "failed", "polling_exhausted")
	}
	now := time.Now()
	err := store.New(b.integration.database).ScheduleWaitingBuild(ctx, store.ScheduleWaitingBuildParams{ID: waiting.ID, NextPollAt: now.Add(delay).UnixMilli(), Reason: reason, Failures: failures, UpdatedAt: now.UnixMilli()})
	return min(delay, time.Hour), err
}
func (b *Builds) retry(ctx context.Context, waiting store.WaitingBuild, cause error) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	b.integration.clearTokensOnFailure(cause)
	switch {
	case errors.Is(cause, routes.ErrOwnership):
		return 0, b.finish(ctx, waiting.ID, "failed", "route_ownership_conflict")
	case errors.Is(cause, routes.ErrSetup):
		return 0, b.finish(ctx, waiting.ID, "failed", "route_setup_required")
	case errors.Is(cause, routes.ErrCapacity):
		return 0, b.finish(ctx, waiting.ID, "failed", "route_capacity")
	case errors.Is(cause, operations.ErrConflict) || errors.Is(cause, projects.ErrBusy) || errors.Is(cause, ErrBusy):
		return b.schedule(ctx, waiting, pollInterval, waiting.Reason, 0)
	case errors.Is(cause, projects.ErrStale):
		return 0, b.finish(ctx, waiting.ID, "superseded", "binding_changed")
	case errors.Is(cause, registry.ErrIncompatible):
		return 0, b.finish(ctx, waiting.ID, "failed", "incompatible_image")
	case errors.Is(cause, ErrOwner) || errors.Is(cause, ErrSetup) || errors.Is(cause, ErrSource) || errors.Is(cause, compose.ErrUnsafe) || errors.Is(cause, compose.ErrMissingSecret) || errors.Is(cause, releases.ErrInvalidArtifact):
		return 0, b.finish(ctx, waiting.ID, "failed", "incompatible_build")
	}
	var failure *Failure
	if errors.As(cause, &failure) && failure.Permanent {
		return 0, b.finish(ctx, waiting.ID, "failed", "provider_rejected")
	}
	if errors.As(cause, &failure) && failure.RateLimited {
		return b.schedule(ctx, waiting, failure.After, waiting.Reason, waiting.Failures)
	}
	count := waiting.Failures + 1
	delay := pollInterval * time.Duration(1<<min(count, 5))
	if errors.As(cause, &failure) && failure.After > delay {
		delay = failure.After
	}
	return b.schedule(ctx, waiting, delay, waiting.Reason, count)
}

// PollBuild turns a waiting deployment into a release once every service's
// GIT_SHA image tag resolves to a digest. Missing tags keep it waiting until
// the bounded image wait expires.
func (b *Builds) PollBuild(ctx context.Context, id string) (time.Duration, error) {
	waiting, err := store.New(b.integration.database).GetWaitingBuild(ctx, id)
	if err != nil {
		return 0, err
	}
	// dispatch_unknown only exists on requests recorded by the former workflow dispatch.
	if waiting.State != "waiting" && waiting.State != "dispatch_unknown" {
		return 0, nil
	}
	now := time.Now()
	if waiting.ExpiresAt <= now.UnixMilli() || waiting.PollCount >= 2880 {
		return 0, b.finish(ctx, id, "failed", expiredReason(waiting.Reason))
	}
	if waiting.NextPollAt > now.UnixMilli() {
		return min(time.Until(time.UnixMilli(waiting.NextPollAt)), time.Hour), nil
	}

	var intent operations.Intent
	var selection projects.Selection
	if json.Unmarshal(waiting.IntentJson, &intent) != nil || json.Unmarshal(waiting.SelectionJson, &selection) != nil || selection.Environment.ID != waiting.EnvironmentID || !sourceSHA(intent.SourceSHA) {
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
	// Avoid repeated provider reads and native config while an earlier effect is active.
	conflicts, err := store.New(b.integration.database).UnresolvedServerOperations(ctx, waiting.ServerID)
	if err != nil {
		return 0, err
	}
	for _, operation := range conflicts {
		if operation.ID != id && (operation.Kind == string(operations.Enroll) || operation.EnvironmentID == waiting.EnvironmentID) {
			return b.schedule(ctx, waiting, pollInterval, waiting.Reason, 0)
		}
	}
	client, repository, err := b.integration.selectedClient(ctx, project.RepositoryID)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	tree, cleanup, err := b.source(ctx, client, repository, selection, intent.SourceSHA)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	defer cleanup()
	artifact, missing, err := b.resolveImages(ctx, tree, selection.Environment.OverridePath, intent.SourceSHA)
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	if missing != "" {
		return b.schedule(ctx, waiting, pollInterval, waitingForImage+missing, 0)
	}
	_, _, err = b.metadata.QueueRelease(ctx, b.runtime, b.resolver, projects.ReleaseInput{Intent: intent, Selection: selection, Build: projects.Build{RepositoryID: project.RepositoryID, Branch: selection.Environment.Branch, SourceSHA: intent.SourceSHA}, Tree: tree, Artifact: artifact})
	if err != nil {
		return b.retry(ctx, waiting, err)
	}
	return 0, nil
}

// resolveImages pins every service image to the digest of its sha tag. It
// returns the first missing reference instead of an error so the caller waits.
func (b *Builds) resolveImages(ctx context.Context, tree, override, sha string) (releases.Artifact, string, error) {
	images, err := compose.Images(tree, override)
	if err != nil {
		return releases.Artifact{}, "", err
	}
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)
	artifact := releases.Artifact{SchemaVersion: 1, SourceSHA: sha, Services: make(map[string]releases.ArtifactService, len(images))}
	digests := make(map[string]string, len(images))
	for _, name := range names {
		repository := images[name]
		digest, resolved := digests[repository]
		if !resolved {
			digest, err = b.images.Resolve(ctx, repository, sha)
			if errors.Is(err, registry.ErrNotFound) {
				return releases.Artifact{}, repository + ":" + sha, nil
			}
			if err != nil {
				return releases.Artifact{}, "", err
			}
			digests[repository] = digest
		}
		artifact.Services[name] = releases.ArtifactService{Image: repository + "@" + digest}
	}
	return artifact, "", nil
}

func expiredReason(reason string) string {
	if image, waiting := strings.CutPrefix(reason, waitingForImage); waiting {
		return imageNotFound + image
	}
	return "build_timeout"
}

func sourceSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20 && strings.ToLower(value) == value
}
